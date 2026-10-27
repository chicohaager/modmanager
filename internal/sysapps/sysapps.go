// Package sysapps hides and restores the apps ZimaOS ships built in.
//
// ZimaOS 1.8 has three kinds of built-in tile, and each needs its own way
// out (all measured on 1.8.0, ZimaBoard2, 2026-10-21):
//
//   - sysext: PeerDrop, Files, ZVM live in /usr/lib/zimaos/modules/<name>.raw,
//     symlinked into /var/lib/extensions. Removing the symlink — which is all
//     the official mod_management uninstall does (zimaos-app-management,
//     (*ModuleStore).ModuleUninstall: os.Remove("/var/lib/extensions/"+name+".raw"))
//     — does not last: /usr/libexec/every-boot.d/01-re-symlink-modules puts a
//     missing link back on every boot. An empty directory /etc/extensions/<name>
//     masks the image instead (systemd-sysext(8)); that script never touches it.
//   - base: Photos is part of the base image on 1.8, /usr is read-only. A
//     directory extension that ships the same manifest with ui.show=false
//     shadows the original; /v2/dashboard/list then drops the tile (the web UI
//     filters with "!module.ui.show:ne:true", Home-*.js). Its services are masked.
//   - system: "Backup" is hard-coded in zimaos-user (dashboard/system map.init.0,
//     zimaapp://system/app/com.zimaos.backup). The owner role carries
//     "app:* visible allow"; a deny tuple for the one resource hides the tile.
//
// Every status reported here is read from the mechanism itself (mask present,
// overlay file present, tuple present), never from a file of our own.
package sysapps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chicohaager/modmanager/internal/progress"
	"github.com/chicohaager/modmanager/internal/state"
)

// Kind is the mechanism an app is hidden with.
type Kind string

const (
	KindSysext Kind = "sysext"
	KindBase   Kind = "base"
	KindSystem Kind = "system"
)

// OverlayName is the directory extension that carries the shadowing manifests.
const OverlayName = "modmanager_overlay"

// BackupResource is the dashboard resource of the built-in Backup tile.
const BackupResource = "zimaapp://system/app/com.zimaos.backup"

// blocked lists modules that must not be hidden, with the reason shown in the UI.
var blocked = map[string]string{
	// icewhale-files and zimaos-search log "require doconverter error" without it (KB §70.1).
	"doconverter": "Files and search depend on it",
}

// blockedCode is the code the UI translates for a module in blocked.
func blockedCode(name string) string {
	if blocked[name] != "" {
		return "dependency"
	}
	return ""
}

// required modules are never listed: ZimaOS needs them (Files is the file
// manager the shell and other apps link into, e.g. the Backup dialog opens
// /modules/icewhale_files/; decision 2026-10-21: not offered at all).
var required = map[string]bool{"icewhale_files": true}

// systemApps are the hard-coded zimaos-user tiles that may be hidden.
// App Store and Settings are left out on purpose: hiding Settings would leave
// no way back through the UI.
var systemApps = []struct{ id, name, title, resource string }{
	{"system:backup", "backup", "Backup", BackupResource},
}

// SystemVersions are the ZimaOS versions on which writing the deny tuple
// into user.db was measured (2026-10-21, v1.8.0). user.db is zimaos-user's
// internal store — there is no supported write path for a deny (the grant
// API's GrantWrite has no effect field) — so the system tiles are offered
// only where the mechanism was verified, and every write is preceded by a
// schema check and a backup of the database.
var SystemVersions = []string{"v1.8.0"}

// tupleColumns are the permission_tuples columns the deny tuple relies on.
var tupleColumns = []string{"namespace", "object_id", "relation", "subject_namespace", "subject_object_id", "subject_relation", "effect", "created_at", "updated_at"}

// keepBackups is how many user.db backups are kept.
const keepBackups = 5

// dataDirs are the data directories an app leaves behind (measured: only Photos
// has one — /DATA/AppData/photos with config, manticore, models, settings).
var dataDirs = map[string]string{
	"icewhale_photos": "/DATA/AppData/photos",
}

// mediaExt marks files that look like user media; purge refuses when it finds one.
var mediaExt = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".heic": true, ".heif": true, ".gif": true,
	".webp": true, ".tif": true, ".tiff": true, ".raw": true, ".dng": true, ".cr2": true,
	".nef": true, ".arw": true, ".mp4": true, ".mov": true, ".mkv": true, ".avi": true, ".m4v": true,
}

// mediaSkip are subtrees of a data directory that hold derived files only
// (Photos keeps previews and models there, never originals).
var mediaSkip = map[string]bool{"models": true, "manticore": true, "cache": true, "thumbnails": true, "previews": true}

// Runner runs a command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs commands on the host.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// Paths are the host locations the manager works on; tests point them into a temp dir.
type Paths struct {
	BuiltinDir string // /usr/lib/zimaos/modules
	ModulesDir string // /usr/share/casaos/modules (merged view)
	EtcExt     string // /etc/extensions
	VarExt     string // /var/lib/extensions
	StateDir   string // state.Dir, root only
	UserDB     string // /var/lib/casaos/db/user.db
	UnitDir    string // /usr/lib/systemd/system (to find sibling units)
	OSRelease  string // /etc/os-release
}

// DefaultPaths are the ZimaOS 1.8 locations.
func DefaultPaths() Paths {
	return Paths{
		BuiltinDir: "/usr/lib/zimaos/modules",
		ModulesDir: "/usr/share/casaos/modules",
		EtcExt:     "/etc/extensions",
		VarExt:     "/var/lib/extensions",
		StateDir:   state.Dir,
		UserDB:     "/var/lib/casaos/db/user.db",
		UnitDir:    "/usr/lib/systemd/system",
		OSRelease:  "/etc/os-release",
	}
}

// BaseReader returns the directory where the base image's manifests can be
// read although extensions are merged over /usr, and a cleanup function.
type BaseReader func(ctx context.Context) (dir string, cleanup func(), err error)

// BindBaseReader bind-mounts / non-recursively: the mount shows the squashfs
// root, i.e. /usr without the sysext overlay (measured on 1.8.0: lists
// icewhale_photos.json and zimanet.json only).
func BindBaseReader(run Runner) BaseReader {
	return func(ctx context.Context) (string, func(), error) {
		tmp, err := os.MkdirTemp("", "modmanager-base-")
		if err != nil {
			return "", nil, err
		}
		if _, err := run(ctx, "mount", "--bind", "/", tmp); err != nil {
			_ = os.Remove(tmp)
			return "", nil, err
		}
		cleanup := func() {
			_, _ = run(context.Background(), "umount", tmp)
			_ = os.Remove(tmp)
		}
		return filepath.Join(tmp, "usr/share/casaos/modules"), cleanup, nil
	}
}

// Manager hides and restores built-in apps. Its methods serialise on one
// lock: systemd-sysext refresh works on the whole /usr.
type Manager struct {
	P    Paths
	Run  Runner
	Base BaseReader
	Now  func() time.Time // tests pin it; nil means time.Now
	// HostNS reports an error when the process does not share the host's
	// mount namespace; tests replace it. nil means SameMountNamespace.
	HostNS func() error
	// Stream runs the commands that change the host with live output for the progress
	// log of the request (progress.FromContext); nil: through Run.
	Stream progress.Stream
	mu     sync.Mutex
}

// change runs a command that changes the host and reports it in the request's progress log.
func (m *Manager) change(ctx context.Context, name string, args ...string) ([]byte, error) {
	return progress.FromContext(ctx).Exec(ctx, m.Stream, m.Run, name, args...)
}

// ErrNamespace: a refresh from a private mount namespace changes nothing the
// dashboard sees.
var ErrNamespace = errors.New("modmanager runs in a private mount namespace; systemd-sysext refresh would not reach the host")

// SameMountNamespace compares this process's mount namespace with PID 1's.
func SameMountNamespace() error {
	self, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return err
	}
	init, err := os.Readlink("/proc/1/ns/mnt")
	if err != nil {
		return err
	}
	if self != init {
		return fmt.Errorf("%w (%s, host %s)", ErrNamespace, self, init)
	}
	return nil
}

func (m *Manager) hostNS() error {
	if m.HostNS != nil {
		return m.HostNS()
	}
	return SameMountNamespace()
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// New returns a Manager on the host.
func New() *Manager {
	return &Manager{P: DefaultPaths(), Run: ExecRunner, Base: BindBaseReader(ExecRunner)}
}

// Service is one systemd unit of an app.
type Service struct {
	Unit   string `json:"unit"`
	Active string `json:"active"`
	Masked bool   `json:"masked"`
}

// Data is an app's data directory.
type Data struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Bytes  int64  `json:"bytes"`
}

// App is one built-in app as the UI shows it.
type App struct {
	ID          string `json:"id"`
	Kind        Kind   `json:"kind"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Icon        string `json:"icon,omitempty"`
	Hidden      bool   `json:"hidden"`
	Blocked     string `json:"blocked,omitempty"`
	BlockedCode string `json:"blocked_code,omitempty"` // dependency | schema | version — the UI translates by code
	// Experimental marks a mechanism that writes into an internal ZimaOS store.
	Experimental bool      `json:"experimental,omitempty"`
	Services     []Service `json:"services"`
	Data         *Data     `json:"data,omitempty"`
}

// manifest is the part of a module manifest we read and rewrite.
type manifest map[string]any

func readManifest(path string) (manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

func (m manifest) ui() map[string]any {
	u, _ := m["ui"].(map[string]any)
	return u
}

// shown reports ui.show == true — the web UI's own criterion for a tile.
func (m manifest) shown() bool {
	u := m.ui()
	v, ok := u["show"].(bool)
	return ok && v
}

func (m manifest) title() string {
	t, _ := m.ui()["title"].(map[string]any)
	if s, ok := t["en_us"].(string); ok && s != "" {
		return s
	}
	for _, v := range t {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func (m manifest) icon() string {
	s, _ := m.ui()["icon"].(string)
	return s
}

func (m manifest) services() []string {
	list, _ := m["services"].([]any)
	var out []string
	for _, e := range list {
		if o, ok := e.(map[string]any); ok {
			if n, ok := o["name"].(string); ok && n != "" {
				if !strings.Contains(n, ".") {
					n += ".service"
				}
				out = append(out, n)
			}
		}
	}
	return out
}

// validName guards every name that ends up in a path or a command.
func validName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		digit := r >= '0' && r <= '9'
		if !letter && !digit && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// overlayManifest is where the shadowing manifest of a base app lives.
func (m *Manager) overlayDir() string { return filepath.Join(m.P.VarExt, OverlayName) }

func (m *Manager) overlayManifest(name string) string {
	return filepath.Join(m.overlayDir(), "usr/share/casaos/modules", name+".json")
}

func (m *Manager) savedManifest(name string) string {
	return filepath.Join(m.P.StateDir, "manifests", name+".json")
}

// List returns every built-in app with its measured status.
func (m *Manager) List(ctx context.Context) ([]App, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.list(ctx)
}

func (m *Manager) list(ctx context.Context) ([]App, error) {
	var apps []App

	// sysext built-ins
	raws, _ := filepath.Glob(filepath.Join(m.P.BuiltinDir, "*.raw"))
	sort.Strings(raws)
	for _, r := range raws {
		name := strings.TrimSuffix(filepath.Base(r), ".raw")
		if !validName(name) || required[name] {
			continue
		}
		a := App{ID: "module:" + name, Kind: KindSysext, Name: name, Title: name, Blocked: blocked[name], BlockedCode: blockedCode(name)}
		a.Hidden = isDir(filepath.Join(m.P.EtcExt, name))
		mf, err := readManifest(filepath.Join(m.P.ModulesDir, name+".json"))
		if err != nil {
			mf, _ = readManifest(m.savedManifest(name)) // masked: the merged copy is gone
		}
		if mf != nil {
			if t := mf.title(); t != "" {
				a.Title = t
			}
			a.Icon = mf.icon()
			a.Services = m.serviceStatus(ctx, mf.services())
		}
		apps = append(apps, a)
	}

	// base-image modules with a tile
	baseDir, cleanup, err := m.Base(ctx)
	if err != nil {
		return nil, fmt.Errorf("read base manifests: %w", err)
	}
	defer cleanup()
	entries, _ := os.ReadDir(baseDir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if !validName(name) || required[name] {
			continue
		}
		mf, err := readManifest(filepath.Join(baseDir, e.Name()))
		if err != nil || !mf.shown() {
			continue // no tile in the base image (zimanet has no ui.show)
		}
		a := App{ID: "module:" + name, Kind: KindBase, Name: name, Title: name, Blocked: blocked[name], BlockedCode: blockedCode(name), Icon: mf.icon()}
		if t := mf.title(); t != "" {
			a.Title = t
		}
		a.Hidden = fileExists(m.overlayManifest(name))
		units := m.relatedUnits(mf.services())
		a.Services = m.serviceStatus(ctx, units)
		if p, ok := dataDirs[name]; ok {
			a.Data = dataStatus(p)
		}
		apps = append(apps, a)
	}

	// hard-coded system tiles
	for _, s := range systemApps {
		a := App{ID: s.id, Kind: KindSystem, Name: s.name, Title: s.title, Experimental: true, Services: []Service{}}
		if err := m.schemaOK(ctx); err != nil {
			a.Blocked, a.BlockedCode = err.Error(), "schema"
		} else {
			hidden, err := m.denyPresent(ctx, s.resource)
			if err != nil {
				return nil, err
			}
			a.Hidden = hidden
			if err := m.versionOK(); err != nil && !hidden {
				a.Blocked, a.BlockedCode = err.Error(), "version" // restoring stays possible: it only removes our own tuple
			}
		}
		apps = append(apps, a)
	}
	for i := range apps {
		if apps[i].Services == nil {
			apps[i].Services = []Service{}
		}
	}
	return apps, nil
}

// relatedUnits adds sibling units that share the manifest service's stem:
// Photos names zimaos-photos.service, but zimaos-photos-ai-worker.service
// runs next to it (measured: RSS 1.8 GB) and must go with it.
func (m *Manager) relatedUnits(units []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range units {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
		stem := strings.TrimSuffix(u, ".service")
		sibs, _ := filepath.Glob(filepath.Join(m.P.UnitDir, stem+"-*.service"))
		sort.Strings(sibs)
		for _, s := range sibs {
			b := filepath.Base(s)
			if !seen[b] {
				seen[b] = true
				out = append(out, b)
			}
		}
	}
	return out
}

func (m *Manager) serviceStatus(ctx context.Context, units []string) []Service {
	out := []Service{}
	for _, u := range units {
		s := Service{Unit: u}
		o, _ := m.Run(ctx, "systemctl", "show", "-p", "ActiveState", "-p", "UnitFileState", "--value", u)
		lines := strings.Split(strings.TrimSpace(string(o)), "\n")
		if len(lines) > 0 {
			s.Active = strings.TrimSpace(lines[0])
		}
		if len(lines) > 1 {
			s.Masked = strings.HasPrefix(strings.TrimSpace(lines[1]), "masked")
		}
		out = append(out, s)
	}
	return out
}

func dataStatus(path string) *Data {
	d := &Data{Path: path}
	if !isDir(path) {
		return d
	}
	d.Exists = true
	_ = filepath.WalkDir(path, func(_ string, e fs.DirEntry, err error) error {
		if err == nil && e.Type().IsRegular() {
			if i, err := e.Info(); err == nil {
				d.Bytes += i.Size()
			}
		}
		return nil
	})
	return d
}

func (m *Manager) find(apps []App, id string) (*App, error) {
	for i := range apps {
		if apps[i].ID == id {
			return &apps[i], nil
		}
	}
	return nil, ErrUnknown
}

// Errors the API maps to status codes.
var (
	ErrUnknown  = errors.New("unknown app")
	ErrBlocked  = errors.New("app must not be hidden")
	ErrNotHid   = errors.New("app is not hidden")
	ErrMedia    = errors.New("data directory contains media files")
	ErrConfirm  = errors.New("confirmation does not match the app name")
	ErrNoOwners = errors.New("no owner account found in the permission table")
)

// Hide hides one app.
func (m *Manager) Hide(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.hostNS(); err != nil {
		return err
	}
	apps, err := m.list(ctx)
	if err != nil {
		return err
	}
	a, err := m.find(apps, id)
	if err != nil {
		return err
	}
	if a.Blocked != "" {
		return fmt.Errorf("%w: %s", ErrBlocked, a.Blocked)
	}
	switch a.Kind {
	case KindSysext:
		return m.hideSysext(ctx, a)
	case KindBase:
		return m.hideBase(ctx, a)
	case KindSystem:
		return m.setDeny(ctx, resourceOf(a.ID), true)
	}
	return ErrUnknown
}

// Restore undoes Hide.
func (m *Manager) Restore(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.hostNS(); err != nil {
		return err
	}
	apps, err := m.list(ctx)
	if err != nil {
		return err
	}
	a, err := m.find(apps, id)
	if err != nil {
		return err
	}
	switch a.Kind {
	case KindSysext:
		return m.restoreSysext(ctx, a)
	case KindBase:
		return m.restoreBase(ctx, a)
	case KindSystem:
		return m.setDeny(ctx, resourceOf(a.ID), false)
	}
	return ErrUnknown
}

func resourceOf(id string) string {
	for _, s := range systemApps {
		if s.id == id {
			return s.resource
		}
	}
	return ""
}

func (m *Manager) saveManifest(name string) error {
	src := filepath.Join(m.P.ModulesDir, name+".json")
	b, err := os.ReadFile(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // nothing merged to keep
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.savedManifest(name)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(m.savedManifest(name), b, 0o644)
}

func (m *Manager) hideSysext(ctx context.Context, a *App) error {
	p := progress.FromContext(ctx)
	p.Stepf("keepManifest", "Keeping the app's manifest for Restore")
	if err := m.saveManifest(a.Name); err != nil {
		return err
	}
	for _, s := range a.Services {
		_, _ = m.change(ctx, "systemctl", "stop", s.Unit)
	}
	p.Stepf("mask", "Masking the system extension {name}", "name", a.Name)
	if err := os.MkdirAll(filepath.Join(m.P.EtcExt, a.Name), 0o755); err != nil {
		return err
	}
	return m.refresh(ctx)
}

func (m *Manager) restoreSysext(ctx context.Context, a *App) error {
	mask := filepath.Join(m.P.EtcExt, a.Name)
	progress.FromContext(ctx).Stepf("unmask", "Removing the mask of {name}", "name", a.Name)
	if isDir(mask) {
		// os.Remove refuses a non-empty directory: a mask someone filled is not ours.
		if err := os.Remove(mask); err != nil {
			return fmt.Errorf("remove mask %s: %w", mask, err)
		}
	}
	if err := m.refresh(ctx); err != nil {
		return err
	}
	for _, s := range a.Services {
		_, _ = m.change(ctx, "systemctl", "start", s.Unit)
	}
	return nil
}

func (m *Manager) hideBase(ctx context.Context, a *App) error {
	progress.FromContext(ctx).Stepf("overlay", "Writing the overlay that hides {name}", "name", a.Name)
	if err := m.writeOverlay(ctx, a.Name); err != nil {
		return err
	}
	for _, s := range a.Services {
		if _, err := m.change(ctx, "systemctl", "disable", "--now", s.Unit); err != nil {
			return err
		}
		if _, err := m.change(ctx, "systemctl", "mask", s.Unit); err != nil {
			return err
		}
	}
	return m.refresh(ctx)
}

func (m *Manager) restoreBase(ctx context.Context, a *App) error {
	progress.FromContext(ctx).Stepf("removeOverlay", "Removing the overlay of {name}", "name", a.Name)
	if err := os.Remove(m.overlayManifest(a.Name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := m.pruneOverlay(); err != nil {
		return err
	}
	if err := m.refresh(ctx); err != nil {
		return err
	}
	for _, s := range a.Services {
		if _, err := m.change(ctx, "systemctl", "unmask", s.Unit); err != nil {
			return err
		}
		if _, err := m.change(ctx, "systemctl", "enable", "--now", s.Unit); err != nil {
			return err
		}
	}
	return nil
}

// writeOverlay writes the shadowing manifest from the base image's current
// one: ui.show=false and no services (so app-management's service
// reconciliation leaves the masked units alone). It reports whether the file
// changed.
func (m *Manager) writeOverlay(ctx context.Context, name string) error {
	_, err := m.syncOverlay(ctx, name)
	return err
}

func (m *Manager) syncOverlay(ctx context.Context, name string) (bool, error) {
	baseDir, cleanup, err := m.Base(ctx)
	if err != nil {
		return false, err
	}
	defer cleanup()
	mf, err := readManifest(filepath.Join(baseDir, name+".json"))
	if err != nil {
		return false, err
	}
	if u := mf.ui(); u != nil {
		u["show"] = false
	}
	delete(mf, "services")
	b, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return false, err
	}
	b = append(b, '\n')
	dst := m.overlayManifest(name)
	if old, err := os.ReadFile(dst); err == nil && string(old) == string(b) {
		return false, nil
	}
	rel := filepath.Join(m.overlayDir(), "usr/lib/extension-release.d", "extension-release."+OverlayName)
	if err := os.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(rel, []byte("ID=_any\n"), 0o644); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(dst, b, 0o644)
}

// pruneOverlay removes the overlay extension when it shadows nothing any more.
func (m *Manager) pruneOverlay() error {
	left, _ := filepath.Glob(filepath.Join(m.overlayDir(), "usr/share/casaos/modules", "*.json"))
	if len(left) > 0 {
		return nil
	}
	if !isDir(m.overlayDir()) {
		return nil
	}
	return os.RemoveAll(m.overlayDir())
}

// Reconcile runs at start: after an OS update the base manifest of a hidden
// app may have changed, and a stale shadow would freeze its old version.
// Returns the names whose overlay was rewritten.
func (m *Manager) Reconcile(ctx context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.hostNS(); err != nil {
		return nil, err
	}
	files, _ := filepath.Glob(filepath.Join(m.overlayDir(), "usr/share/casaos/modules", "*.json"))
	var changed []string
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		if !validName(name) {
			continue
		}
		c, err := m.syncOverlay(ctx, name)
		if err != nil {
			return changed, fmt.Errorf("%s: %w", name, err)
		}
		if c {
			changed = append(changed, name)
		}
	}
	if len(changed) > 0 {
		return changed, m.refresh(ctx)
	}
	return nil, nil
}

func (m *Manager) refresh(ctx context.Context) error {
	progress.FromContext(ctx).Stepf("refresh", "ZimaOS merges its system extensions again")
	if _, err := m.change(ctx, "systemd-sysext", "refresh"); err != nil {
		return err
	}
	_, err := m.change(ctx, "systemctl", "daemon-reload")
	return err
}

// --- system tiles: permission tuples in zimaos-user's database ---

func (m *Manager) sql(ctx context.Context, q string) (string, error) {
	// zimaos-user holds the database open; wait for its lock instead of failing.
	out, err := m.Run(ctx, "sqlite3", "-batch", "-cmd", ".timeout 5000", m.P.UserDB, q)
	return strings.TrimSpace(string(out)), err
}

// owners returns the user ids that hold the owner role.
func (m *Manager) owners(ctx context.Context) ([]int, error) {
	out, err := m.sql(ctx, "select subject_object_id from permission_tuples where namespace='role' and object_id='owner' and relation='member' and subject_namespace='user' and effect='allow';")
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, l := range strings.Fields(out) {
		n, err := strconv.Atoi(l)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("unexpected owner id %q", l)
		}
		ids = append(ids, n)
	}
	if len(ids) == 0 {
		return nil, ErrNoOwners
	}
	return ids, nil
}

func (m *Manager) denyPresent(ctx context.Context, resource string) (bool, error) {
	out, err := m.sql(ctx, "select count(*) from permission_tuples where namespace='app' and object_id='"+resource+"' and relation='visible' and effect='deny';")
	if err != nil {
		return false, err
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return false, fmt.Errorf("unexpected count %q", out)
	}
	return n > 0, nil
}

// versionOK reports whether this ZimaOS version is one the deny tuple was verified on.
func (m *Manager) versionOK() error {
	b, err := os.ReadFile(m.P.OSRelease)
	if err != nil {
		return fmt.Errorf("ZimaOS version unknown: %w", err)
	}
	v := ""
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VERSION=") {
			v = strings.Trim(strings.TrimPrefix(l, "VERSION="), `"'`)
		}
	}
	for _, ok := range SystemVersions {
		if v == ok {
			return nil
		}
	}
	return fmt.Errorf("not verified on ZimaOS %s (verified: %s)", v, strings.Join(SystemVersions, ", "))
}

// schemaOK checks the parts of user.db the deny tuple touches before anything
// reads or writes them: a changed schema means the mechanism is not the one
// that was measured.
func (m *Manager) schemaOK(ctx context.Context) error {
	cols, err := m.sql(ctx, "select name from pragma_table_info('permission_tuples');")
	if err != nil {
		return fmt.Errorf("user database not readable: %w", err)
	}
	have := map[string]bool{}
	for _, c := range strings.Fields(cols) {
		have[c] = true
	}
	for _, c := range tupleColumns {
		if !have[c] {
			return fmt.Errorf("user database schema changed: permission_tuples has no column %s", c)
		}
	}
	n, err := m.sql(ctx, "select count(*) from pragma_table_info('permission_generation') where name='value';")
	if err != nil || n != "1" {
		return fmt.Errorf("user database schema changed: no permission_generation.value")
	}
	if _, err := m.owners(ctx); err != nil {
		return err
	}
	return nil
}

// backupUserDB copies user.db with sqlite's online backup (consistent while
// zimaos-user has it open) into StateDir/userdb-backups, mode 0600, and keeps
// the newest keepBackups copies.
func (m *Manager) backupUserDB(ctx context.Context) (string, error) {
	dir := filepath.Join(m.P.StateDir, "userdb-backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "user.db."+m.now().UTC().Format("20060102T150405.000000000Z"))
	if _, err := m.Run(ctx, "sqlite3", "-batch", m.P.UserDB, ".backup '"+dst+"'"); err != nil {
		return "", fmt.Errorf("backup of user database failed, nothing written: %w", err)
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return "", err
	}
	old, _ := filepath.Glob(filepath.Join(dir, "user.db.*"))
	sort.Strings(old)
	for len(old) > keepBackups {
		_ = os.Remove(old[0])
		old = old[1:]
	}
	return dst, nil
}

// setDeny adds or removes the deny tuple for every owner and bumps the
// permission generation, which makes zimaos-user reload its snapshot
// (measured: the tile left /v2/dashboard/list at once).
func (m *Manager) setDeny(ctx context.Context, resource string, deny bool) error {
	if resource == "" {
		return ErrUnknown
	}
	if err := m.schemaOK(ctx); err != nil {
		return err
	}
	if deny {
		if err := m.versionOK(); err != nil {
			return fmt.Errorf("%w: %w", ErrBlocked, err)
		}
	}
	p := progress.FromContext(ctx)
	dst, err := m.backupUserDB(ctx)
	if err != nil {
		return err
	}
	p.Okf("userdbBackup", "User database backed up: {path}", "path", dst)
	if deny {
		p.Stepf("tileHide", "Hiding the tile for every user in the ZimaOS user database")
	} else {
		p.Stepf("tileShow", "Showing the tile again in the ZimaOS user database")
	}
	var q strings.Builder
	q.WriteString("begin;")
	if deny {
		ids, err := m.owners(ctx)
		if err != nil {
			return err
		}
		for _, id := range ids {
			fmt.Fprintf(&q, "insert into permission_tuples(namespace,object_id,relation,subject_namespace,subject_object_id,subject_relation,effect,created_at,updated_at) "+
				"select 'app','%s','visible','user','%d','','deny',datetime('now'),datetime('now') "+
				"where not exists (select 1 from permission_tuples where namespace='app' and object_id='%s' and relation='visible' and subject_namespace='user' and subject_object_id='%d' and subject_relation='');",
				resource, id, resource, id)
		}
	} else {
		fmt.Fprintf(&q, "delete from permission_tuples where namespace='app' and object_id='%s' and relation='visible' and effect='deny';", resource)
	}
	q.WriteString("update permission_generation set value=value+1, updated_at=datetime('now');commit;")
	_, err = m.sql(ctx, q.String())
	return err
}

// --- data ---

// Purge deletes the data directory of a hidden app. confirm must repeat the
// app's name. It refuses when it finds a media file outside the derived
// subtrees: the photo library itself lives elsewhere on ZimaOS, a photo here
// would be something the user put there.
func (m *Manager) Purge(ctx context.Context, id, confirm string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	apps, err := m.list(ctx)
	if err != nil {
		return err
	}
	a, err := m.find(apps, id)
	if err != nil {
		return err
	}
	if !a.Hidden {
		return ErrNotHid
	}
	if confirm != a.Name {
		return ErrConfirm
	}
	if a.Data == nil || !a.Data.Exists {
		return nil
	}
	p := progress.FromContext(ctx)
	p.Stepf("mediaCheck", "Looking for photos or videos in {path}", "path", a.Data.Path)
	f, err := findMedia(a.Data.Path)
	if err != nil {
		// A subtree the walk cannot read is one it cannot vouch for.
		return fmt.Errorf("data directory not fully readable, nothing deleted: %w", err)
	}
	if f != "" {
		return fmt.Errorf("%w: %s", ErrMedia, f)
	}
	p.Okf("noMedia", "No photo or video found")
	p.Stepf("delete", "Deleting {path}", "path", a.Data.Path)
	return os.RemoveAll(a.Data.Path)
}

func findMedia(root string) (string, error) {
	var hit string
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && p != root && mediaSkip[strings.ToLower(e.Name())] {
			return filepath.SkipDir
		}
		if e.Type().IsRegular() && mediaExt[strings.ToLower(filepath.Ext(p))] {
			hit = p
			return filepath.SkipAll
		}
		return nil
	})
	return hit, err
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
