package sysapps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHost is a temp-dir ZimaOS: built-in sysexts, a merged modules dir, a
// base-image modules dir, unit files and (optionally) a real sqlite user.db
// with the schema measured on 1.8.0.
type fakeHost struct {
	t    *testing.T
	root string
	base string
	mu   sync.Mutex
	cmds []string
	m    *Manager
}

func newHost(t *testing.T) *fakeHost {
	t.Helper()
	h := &fakeHost{t: t, root: t.TempDir()}
	p := Paths{
		BuiltinDir: h.path("usr/lib/zimaos/modules"),
		ModulesDir: h.path("usr/share/casaos/modules"),
		EtcExt:     h.path("etc/extensions"),
		VarExt:     h.path("var/lib/extensions"),
		StateDir:   h.path("DATA/AppData/modmanager"),
		UserDB:     h.path("var/lib/casaos/db/user.db"),
		UnitDir:    h.path("usr/lib/systemd/system"),
		OSRelease:  h.path("etc/os-release"),
	}
	h.base = h.path("base/usr/share/casaos/modules")
	for _, d := range []string{p.BuiltinDir, p.ModulesDir, p.VarExt, p.UnitDir, h.base, filepath.Dir(p.UserDB)} {
		must(t, os.MkdirAll(d, 0o755))
	}
	must(t, os.MkdirAll(filepath.Dir(p.OSRelease), 0o755))
	must(t, os.WriteFile(p.OSRelease, []byte("NAME=\"ZimaOS\"\nVERSION=\"v1.8.0\"\n"), 0o644))
	tick := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	h.m = &Manager{
		HostNS: func() error { return nil },
		Now:    func() time.Time { tick = tick.Add(time.Second); return tick },
		P:      p,
		Run:    h.run,
		Base: func(context.Context) (string, func(), error) {
			return h.base, func() {}, nil
		},
	}
	return h
}

func (h *fakeHost) path(rel string) string { return filepath.Join(h.root, rel) }

func (h *fakeHost) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	h.cmds = append(h.cmds, name+" "+strings.Join(args, " "))
	h.mu.Unlock()
	switch name {
	case "sqlite3":
		return ExecRunner(ctx, name, args...)
	case "systemctl":
		if len(args) > 0 && args[0] == "show" {
			return []byte("active\nenabled\n"), nil
		}
	}
	return nil, nil
}

func (h *fakeHost) ran(prefix string) bool {
	for _, c := range h.cmds {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	must(t, err)
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(t, os.WriteFile(path, b, 0o644))
}

func tile(name, title string, show bool, services ...string) map[string]any {
	ui := map[string]any{"name": name, "title": map[string]any{"en_us": title}, "icon": "/modules/" + name + "/icon.png"}
	if show {
		ui["show"] = true
	}
	m := map[string]any{"name": name, "ui": ui}
	if len(services) > 0 {
		var s []any
		for _, n := range services {
			s = append(s, map[string]any{"name": n})
		}
		m["services"] = s
	}
	return m
}

// world builds the 1.8.0 layout measured on 2026-10-21: three built-in
// sysexts (PeerDrop, Files, ZVM), Photos + zimanet in the base image (only
// Photos has ui.show), the Photos AI worker as a sibling unit.
func (h *fakeHost) world() {
	t := h.t
	for _, n := range []string{"icewhale_peerdrop", "icewhale_files", "zimaos_zvm"} {
		must(t, os.WriteFile(filepath.Join(h.m.P.BuiltinDir, n+".raw"), []byte("x"), 0o644))
		writeJSON(t, filepath.Join(h.m.P.ModulesDir, n+".json"), tile(n, strings.TrimPrefix(n, "icewhale_"), true))
	}
	writeJSON(t, filepath.Join(h.base, "icewhale_photos.json"), tile("icewhale_photos", "Photos", true, "zimaos-photos.service"))
	writeJSON(t, filepath.Join(h.base, "zimanet.json"), map[string]any{"name": "zimanet", "services": []any{map[string]any{"name": "znet"}}})
	for _, u := range []string{"zimaos-photos.service", "zimaos-photos-ai-worker.service"} {
		must(t, os.WriteFile(filepath.Join(h.m.P.UnitDir, u), []byte("[Unit]\n"), 0o644))
	}
}

func (h *fakeHost) userDB() {
	h.t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		h.t.Skip("sqlite3 not installed")
	}
	schema := "CREATE TABLE `permission_tuples` (`id` integer PRIMARY KEY AUTOINCREMENT,`namespace` varchar(32) NOT NULL,`object_id` varchar(1024) NOT NULL,`relation` varchar(32) NOT NULL,`subject_namespace` varchar(32) NOT NULL,`subject_object_id` varchar(255) NOT NULL,`subject_relation` varchar(32) NOT NULL DEFAULT \"\",`effect` varchar(8) NOT NULL DEFAULT \"allow\",`created_at` datetime,`updated_at` datetime);" +
		"CREATE UNIQUE INDEX `ux_permission_tuple` ON `permission_tuples`(`namespace`,`object_id`,`relation`,`subject_namespace`,`subject_object_id`,`subject_relation`);" +
		"CREATE TABLE `permission_generation` (`id` integer PRIMARY KEY AUTOINCREMENT,`value` integer NOT NULL DEFAULT 0,`updated_at` datetime);" +
		"INSERT INTO permission_generation(value) VALUES (100);" +
		"INSERT INTO permission_tuples(namespace,object_id,relation,subject_namespace,subject_object_id,subject_relation,effect) VALUES" +
		"('app','*','visible','role','owner','member','allow'),('role','owner','member','user','1','','allow');"
	if out, err := exec.Command("sqlite3", h.m.P.UserDB, schema).CombinedOutput(); err != nil {
		h.t.Fatalf("schema: %v %s", err, out)
	}
}

func (h *fakeHost) q(sql string) string {
	out, err := exec.Command("sqlite3", h.m.P.UserDB, sql).CombinedOutput()
	if err != nil {
		h.t.Fatalf("%s: %v %s", sql, err, out)
	}
	return strings.TrimSpace(string(out))
}

func byID(t *testing.T, apps []App, id string) App {
	t.Helper()
	for _, a := range apps {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("app %s not listed (got %d apps)", id, len(apps))
	return App{}
}

func TestListFindsAllThreeKindsAndOnlyTiles(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	apps, err := h.m.List(context.Background())
	must(t, err)
	want := map[string]Kind{
		"module:icewhale_peerdrop": KindSysext, "module:zimaos_zvm": KindSysext,
		"module:icewhale_photos": KindBase, "system:backup": KindSystem,
	}
	if len(apps) != len(want) {
		t.Fatalf("got %d apps, want %d: %+v", len(apps), len(want), apps)
	}
	for id, k := range want {
		if a := byID(t, apps, id); a.Kind != k || a.Hidden {
			t.Errorf("%s: kind %s hidden %v, want %s visible", id, a.Kind, a.Hidden, k)
		}
	}
	photos := byID(t, apps, "module:icewhale_photos")
	if len(photos.Services) != 2 || photos.Services[1].Unit != "zimaos-photos-ai-worker.service" {
		t.Errorf("photos services %+v, want the manifest unit plus its ai-worker sibling", photos.Services)
	}
	if photos.Data == nil || photos.Data.Path != "/DATA/AppData/photos" {
		t.Errorf("photos data %+v", photos.Data)
	}
}

func TestHideAndRestoreSysextMasksWithEmptyDir(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	ctx := context.Background()
	must(t, h.m.Hide(ctx, "module:icewhale_peerdrop"))
	mask := filepath.Join(h.m.P.EtcExt, "icewhale_peerdrop")
	if !isDir(mask) {
		t.Fatal("mask directory not created")
	}
	if !h.ran("systemd-sysext refresh") || !h.ran("systemctl daemon-reload") {
		t.Fatalf("no refresh: %v", h.cmds)
	}
	// systemd-sysext refresh removes the merged manifest; the title must survive.
	must(t, os.Remove(filepath.Join(h.m.P.ModulesDir, "icewhale_peerdrop.json")))
	apps, err := h.m.List(ctx)
	must(t, err)
	if a := byID(t, apps, "module:icewhale_peerdrop"); !a.Hidden || a.Title != "peerdrop" {
		t.Fatalf("after hide: %+v", a)
	}
	must(t, h.m.Restore(ctx, "module:icewhale_peerdrop"))
	if isDir(mask) {
		t.Fatal("mask still there after restore")
	}
}

func TestRestoreLeavesAForeignNonEmptyMaskAlone(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	mask := filepath.Join(h.m.P.EtcExt, "zimaos_zvm")
	must(t, os.MkdirAll(filepath.Join(mask, "usr"), 0o755))
	if err := h.m.Restore(context.Background(), "module:zimaos_zvm"); err == nil {
		t.Fatal("restore removed or ignored a non-empty directory that is not our mask")
	}
	if !isDir(filepath.Join(mask, "usr")) {
		t.Fatal("foreign extension content deleted")
	}
}

func TestHideBaseShadowsManifestAndMasksUnits(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	ctx := context.Background()
	must(t, h.m.Hide(ctx, "module:icewhale_photos"))
	ov, err := readManifest(h.m.overlayManifest("icewhale_photos"))
	must(t, err)
	if ov.shown() {
		t.Error("overlay manifest still has ui.show=true")
	}
	if _, ok := ov["services"]; ok {
		t.Error("overlay manifest still lists services")
	}
	if ov.title() != "Photos" {
		t.Errorf("overlay lost the title: %q", ov.title())
	}
	rel := filepath.Join(h.m.overlayDir(), "usr/lib/extension-release.d/extension-release."+OverlayName)
	if b, err := os.ReadFile(rel); err != nil || string(b) != "ID=_any\n" {
		t.Errorf("extension-release: %q %v", b, err)
	}
	for _, u := range []string{"zimaos-photos.service", "zimaos-photos-ai-worker.service"} {
		if !h.ran("systemctl mask "+u) || !h.ran("systemctl disable --now "+u) {
			t.Errorf("%s not disabled and masked: %v", u, h.cmds)
		}
	}
	apps, err := h.m.List(ctx)
	must(t, err)
	if !byID(t, apps, "module:icewhale_photos").Hidden {
		t.Error("photos not reported hidden")
	}
	must(t, h.m.Restore(ctx, "module:icewhale_photos"))
	if isDir(h.m.overlayDir()) {
		t.Error("empty overlay extension left behind")
	}
	if !h.ran("systemctl unmask zimaos-photos-ai-worker.service") || !h.ran("systemctl enable --now zimaos-photos.service") {
		t.Errorf("units not restored: %v", h.cmds)
	}
}

func TestReconcileFollowsTheBaseManifestAfterAnUpdate(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	ctx := context.Background()
	must(t, h.m.Hide(ctx, "module:icewhale_photos"))
	h.cmds = nil
	changed, err := h.m.Reconcile(ctx)
	must(t, err)
	if len(changed) != 0 || h.ran("systemd-sysext refresh") {
		t.Fatalf("unchanged base caused a rewrite %v / refresh %v", changed, h.cmds)
	}
	up := tile("icewhale_photos", "Photos 2", true, "zimaos-photos.service")
	writeJSON(t, filepath.Join(h.base, "icewhale_photos.json"), up)
	changed, err = h.m.Reconcile(ctx)
	must(t, err)
	ov, _ := readManifest(h.m.overlayManifest("icewhale_photos"))
	if len(changed) != 1 || ov.title() != "Photos 2" || ov.shown() {
		t.Fatalf("changed %v, overlay title %q shown %v", changed, ov.title(), ov.shown())
	}
	if !h.ran("systemd-sysext refresh") {
		t.Error("rewritten overlay not merged")
	}
}

func TestSystemTileDenyTupleForEveryOwner(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	h.q("INSERT INTO permission_tuples(namespace,object_id,relation,subject_namespace,subject_object_id,subject_relation,effect) VALUES ('role','owner','member','user','7','','allow');")
	ctx := context.Background()
	must(t, h.m.Hide(ctx, "system:backup"))
	if got := h.q("select group_concat(subject_object_id) from (select subject_object_id from permission_tuples where object_id='" + BackupResource + "' and relation='visible' and effect='deny' order by 1);"); got != "1,7" {
		t.Fatalf("deny tuples for %q, want 1,7", got)
	}
	if g := h.q("select value from permission_generation"); g != "101" {
		t.Errorf("generation %s, want 101", g)
	}
	must(t, h.m.Hide(ctx, "system:backup")) // idempotent
	if n := h.q("select count(*) from permission_tuples where effect='deny'"); n != "2" {
		t.Errorf("second hide made %s deny tuples", n)
	}
	apps, err := h.m.List(ctx)
	must(t, err)
	if !byID(t, apps, "system:backup").Hidden {
		t.Error("backup not reported hidden")
	}
	must(t, h.m.Restore(ctx, "system:backup"))
	if n := h.q("select count(*) from permission_tuples where effect='deny'"); n != "0" {
		t.Errorf("%s deny tuples left", n)
	}
	if n := h.q("select count(*) from permission_tuples"); n != "3" {
		t.Errorf("restore touched other tuples: %s left, want 3", n)
	}
}

func TestBlockedModuleIsRefused(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	must(t, os.WriteFile(filepath.Join(h.m.P.BuiltinDir, "doconverter.raw"), []byte("x"), 0o644))
	err := h.m.Hide(context.Background(), "module:doconverter")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("got %v, want ErrBlocked", err)
	}
	if isDir(filepath.Join(h.m.P.EtcExt, "doconverter")) {
		t.Fatal("doconverter masked anyway")
	}
	apps, err := h.m.List(context.Background())
	must(t, err)
	if a := byID(t, apps, "module:doconverter"); a.BlockedCode != "dependency" {
		t.Errorf("doconverter blocked code %q, want dependency (the UI translates by code)", a.BlockedCode)
	}
}

func TestPurgeGuards(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	ctx := context.Background()
	data := h.path("photosdata")
	dataDirs["icewhale_photos"] = data
	t.Cleanup(func() { dataDirs["icewhale_photos"] = "/DATA/AppData/photos" })
	must(t, os.MkdirAll(filepath.Join(data, "manticore"), 0o755))
	must(t, os.WriteFile(filepath.Join(data, "manticore", "idx.spa"), []byte("x"), 0o644))
	must(t, os.MkdirAll(filepath.Join(data, "models"), 0o755))
	must(t, os.WriteFile(filepath.Join(data, "models", "preview.jpg"), []byte("x"), 0o644)) // derived subtree

	if err := h.m.Purge(ctx, "module:icewhale_photos", "icewhale_photos"); !errors.Is(err, ErrNotHid) {
		t.Fatalf("purge of a visible app: %v", err)
	}
	must(t, h.m.Hide(ctx, "module:icewhale_photos"))
	if err := h.m.Purge(ctx, "module:icewhale_photos", "photos"); !errors.Is(err, ErrConfirm) {
		t.Fatalf("wrong confirmation: %v", err)
	}
	must(t, os.MkdirAll(filepath.Join(data, "config"), 0o755))
	must(t, os.WriteFile(filepath.Join(data, "config", "IMG_0001.HEIC"), []byte("x"), 0o644))
	if err := h.m.Purge(ctx, "module:icewhale_photos", "icewhale_photos"); !errors.Is(err, ErrMedia) {
		t.Fatalf("media file not caught: %v", err)
	}
	if !isDir(data) {
		t.Fatal("data deleted despite a media file")
	}
	must(t, os.Remove(filepath.Join(data, "config", "IMG_0001.HEIC")))
	must(t, h.m.Purge(ctx, "module:icewhale_photos", "icewhale_photos"))
	if isDir(data) {
		t.Fatal("data directory still there")
	}
}

func TestValidNameRejectsPathsAndQuotes(t *testing.T) {
	for _, s := range []string{"", "../etc", "a/b", "x'y", "a b", strings.Repeat("a", 65)} {
		if validName(s) {
			t.Errorf("validName(%q) = true", s)
		}
	}
	for _, s := range []string{"icewhale_photos", "zimaos-zvm", "Cron2"} {
		if !validName(s) {
			t.Errorf("validName(%q) = false", s)
		}
	}
}

func (h *fakeHost) backups() []string {
	b, _ := filepath.Glob(filepath.Join(h.m.P.StateDir, "userdb-backups", "user.db.*"))
	return b
}

func TestSystemTileBacksUpUserDBBeforeEveryWrite(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	ctx := context.Background()
	must(t, h.m.Hide(ctx, "system:backup"))
	b := h.backups()
	if len(b) != 1 {
		t.Fatalf("%d backups after one write, want 1", len(b))
	}
	st, err := os.Stat(b[0])
	must(t, err)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("backup mode %o, want 600", st.Mode().Perm())
	}
	dst, err := os.Stat(filepath.Dir(b[0]))
	must(t, err)
	if dst.Mode().Perm() != 0o700 {
		t.Errorf("backup dir mode %o, want 700", dst.Mode().Perm())
	}
	// the backup is the state before the write: no deny tuple in it
	out, err := exec.Command("sqlite3", b[0], "select count(*) from permission_tuples where effect='deny'").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "0" {
		t.Errorf("backup holds %q deny tuples (%v), want the pre-write state 0", out, err)
	}
	for i := 0; i < 7; i++ {
		must(t, h.m.Restore(ctx, "system:backup"))
	}
	if n := len(h.backups()); n != keepBackups {
		t.Errorf("%d backups kept, want %d", n, keepBackups)
	}
	apps, err := h.m.List(ctx)
	must(t, err)
	if a := byID(t, apps, "system:backup"); !a.Experimental {
		t.Error("system tile not marked experimental")
	}
}

func TestSystemTileRefusedOnAnUnverifiedVersionButRestorable(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	ctx := context.Background()
	must(t, h.m.Hide(ctx, "system:backup")) // on v1.8.0
	must(t, os.WriteFile(h.m.P.OSRelease, []byte("VERSION=\"v1.8.1\"\n"), 0o644))
	apps, err := h.m.List(ctx)
	must(t, err)
	if a := byID(t, apps, "system:backup"); a.Blocked != "" {
		t.Errorf("hidden tile on a new version must stay restorable, got blocked %q", a.Blocked)
	}
	must(t, h.m.Restore(ctx, "system:backup"))
	if n := h.q("select count(*) from permission_tuples where effect='deny'"); n != "0" {
		t.Fatalf("restore on unverified version left %s deny tuples", n)
	}
	apps, err = h.m.List(ctx)
	must(t, err)
	if a := byID(t, apps, "system:backup"); !strings.Contains(a.Blocked, "v1.8.1") || a.BlockedCode != "version" {
		t.Errorf("visible tile on v1.8.1 not blocked with code version: %q %q", a.Blocked, a.BlockedCode)
	}
	before := len(h.backups())
	if err := h.m.Hide(ctx, "system:backup"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("hide on v1.8.1: %v, want ErrBlocked", err)
	}
	if n := h.q("select count(*) from permission_tuples where effect='deny'"); n != "0" {
		t.Errorf("hide on an unverified version wrote %s deny tuples", n)
	}
	if len(h.backups()) != before {
		t.Error("refused hide still made a backup")
	}
}

func TestSystemTileRefusedWhenTheSchemaChanged(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	// an update that renames a column
	h.q("ALTER TABLE permission_tuples RENAME COLUMN subject_relation TO subject_rel;")
	ctx := context.Background()
	apps, err := h.m.List(ctx)
	must(t, err)
	if a := byID(t, apps, "system:backup"); !strings.Contains(a.Blocked, "subject_relation") {
		t.Errorf("changed schema not reported: %q", a.Blocked)
	}
	if err := h.m.Hide(ctx, "system:backup"); err == nil {
		t.Fatal("hide went ahead on a changed schema")
	}
	if n := h.q("select count(*) from permission_tuples"); n != "2" {
		t.Errorf("tuples changed to %s on a refused write", n)
	}
	if len(h.backups()) != 0 {
		t.Error("backup made although the write was refused before it")
	}
}

func TestRestoreRefusedWhenTheSchemaChangedAfterHide(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	ctx := context.Background()
	must(t, h.m.Hide(ctx, "system:backup"))
	h.q("ALTER TABLE permission_tuples RENAME COLUMN subject_relation TO subject_rel;")
	if err := h.m.Restore(ctx, "system:backup"); err == nil {
		t.Fatal("restore wrote into a changed schema")
	}
	if n := h.q("select count(*) from permission_tuples"); n != "3" {
		t.Errorf("tuples changed to %s on a refused restore", n)
	}
}

// setDeny guards itself, independent of the Blocked flag List computes.
func TestSetDenyRefusesAnUnverifiedVersionOnItsOwn(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	must(t, os.WriteFile(h.m.P.OSRelease, []byte("VERSION=\"v1.9.0\"\n"), 0o644))
	if err := h.m.setDeny(context.Background(), BackupResource, true); !errors.Is(err, ErrBlocked) {
		t.Fatalf("setDeny on v1.9.0: %v, want ErrBlocked", err)
	}
	if n := h.q("select count(*) from permission_tuples where effect='deny'"); n != "0" {
		t.Errorf("%s deny tuples written", n)
	}
}

func TestActionsRefuseAPrivateMountNamespace(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	h.m.HostNS = func() error { return ErrNamespace }
	ctx := context.Background()
	for _, id := range []string{"module:icewhale_peerdrop", "module:icewhale_photos", "system:backup"} {
		if err := h.m.Hide(ctx, id); !errors.Is(err, ErrNamespace) {
			t.Errorf("hide %s in a private namespace: %v", id, err)
		}
		if err := h.m.Restore(ctx, id); !errors.Is(err, ErrNamespace) {
			t.Errorf("restore %s in a private namespace: %v", id, err)
		}
	}
	if _, err := h.m.Reconcile(ctx); !errors.Is(err, ErrNamespace) {
		t.Errorf("reconcile in a private namespace: %v", err)
	}
	if isDir(filepath.Join(h.m.P.EtcExt, "icewhale_peerdrop")) || isDir(h.m.overlayDir()) || h.ran("systemd-sysext") || h.ran("systemctl mask") {
		t.Fatalf("something was changed anyway: %v", h.cmds)
	}
}

func TestPurgeRefusesAnUnreadableSubtree(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	h := newHost(t)
	h.world()
	h.userDB()
	ctx := context.Background()
	data := h.path("photosdata")
	dataDirs["icewhale_photos"] = data
	t.Cleanup(func() { dataDirs["icewhale_photos"] = "/DATA/AppData/photos" })
	locked := filepath.Join(data, "config")
	must(t, os.MkdirAll(locked, 0o755))
	must(t, os.WriteFile(filepath.Join(locked, "IMG_0002.JPG"), []byte("x"), 0o644))
	must(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	must(t, h.m.Hide(ctx, "module:icewhale_photos"))
	err := h.m.Purge(ctx, "module:icewhale_photos", "icewhale_photos")
	if err == nil || !strings.Contains(err.Error(), "not fully readable") {
		t.Fatalf("purge must refuse because the search could not read a subtree, got: %v", err)
	}
	must(t, os.Chmod(locked, 0o755))
	if _, err := os.Stat(filepath.Join(locked, "IMG_0002.JPG")); err != nil {
		t.Fatalf("file behind the unreadable directory is gone: %v", err)
	}
}

func TestFilesIsNeverListedNorHideable(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	ctx := context.Background()
	apps, err := h.m.List(ctx)
	must(t, err)
	for _, a := range apps {
		if a.Name == "icewhale_files" {
			t.Fatalf("Files is listed: %+v", a)
		}
	}
	if err := h.m.Hide(ctx, "module:icewhale_files"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("hide Files: %v, want ErrUnknown", err)
	}
	if isDir(filepath.Join(h.m.P.EtcExt, "icewhale_files")) {
		t.Fatal("Files masked anyway")
	}
}
