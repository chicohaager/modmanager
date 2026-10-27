// Package catalog lists installable sysext modules and installs them with
// zpkg, IceWhale's own module CLI (measured on 1.8.0: `zpkg install <name>`
// from the configured catalogs, `zpkg install <file>.raw` for a local image).
//
// Two sources:
//   - Lintux presets: the Lintux module repositories (measured 2026-10-21 which
//     publish an image: cron, zima-backup, web-ftp-client and, since 2026-10-23,
//     admintools, wireguard, nebula, restic and, since 2026-10-26, pangolin and
//     node-exporter (Prometheus Exporter) install via zpkg;
//     tailscale, mergerfs-snapraid and zfw need their own installer and are
//     shown with its command instead of an install button).
//   - Repositories the user adds (owner/repo or a github.com URL): the newest
//     release's <name>[-_]<arch>.raw or <name>.raw, installed only when the
//     release ships a checksum for it.
//
// The Mod Store list app-management offers is deliberately not shown
// (decision 2026-10-21).
package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chicohaager/lintux-modkit/stale"
	"github.com/chicohaager/modmanager/internal/progress"
	"github.com/chicohaager/modmanager/internal/state"
)

// Runner runs a command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Preset is a known repository.
type Preset struct {
	Repo      string
	Title     string
	Installer string // "zpkg" or "own"
	Command   string // for Installer "own"
	// Where the installed version can be read (lintux-modkit install.sh,
	// installed_version): a health route behind the gateway that answers
	// {"version": …} without login, or a key in the module's extension-release.
	HealthPath string
	ReleaseKey string
}

const lintuxInstall = "curl -fsSL https://raw.githubusercontent.com/chicohaager/lintux-modkit/main/install.sh -o /tmp/lintux-install.sh && sudo bash /tmp/lintux-install.sh --install "

// Presets are the Lintux module repositories with a release image (gh api, 2026-10-21;
// admintools, wireguard, nebula and restic since 2026-10-23, pangolin and node-exporter since 2026-10-26).
var Presets = []Preset{
	{Repo: "chicohaager/cron", Title: "Cron", Installer: "zpkg", HealthPath: "/cron/health"},
	{Repo: "chicohaager/zima-backup", Title: "Sync & Backup", Installer: "zpkg", HealthPath: "/v2/zbackup/api/health"},
	{Repo: "chicohaager/web-ftp-client", Title: "Web FTP Client", Installer: "zpkg"},
	{Repo: "chicohaager/zimaos-admintools-sysext", Title: "Admin Tools", Installer: "zpkg", HealthPath: "/admintools/health"},
	{Repo: "chicohaager/zimaos-wireguard-sysext", Title: "WireGuard", Installer: "zpkg", HealthPath: "/wireguard/health"},
	{Repo: "chicohaager/zimaos-nebula-sysext", Title: "Nebula", Installer: "zpkg", HealthPath: "/nebula/health"},
	{Repo: "chicohaager/zimaos-restic-sysext", Title: "restic Backup", Installer: "zpkg", HealthPath: "/restic/health"},
	{Repo: "chicohaager/zimaos-pangolin-sysext", Title: "Pangolin", Installer: "zpkg", HealthPath: "/pangolin/health"},
	{Repo: "chicohaager/zimaos-node-exporter-sysext", Title: "Prometheus Exporter", Installer: "zpkg", HealthPath: "/node-exporter/health"},
	{Repo: "chicohaager/zfw", Title: "ZFW Firewall", Installer: "own", Command: lintuxInstall + "zfw", HealthPath: "/v2/zfw/api/health"},
	{Repo: "chicohaager/zimaos-mergerfs-snapraid-sysext", Title: "mergerfs + SnapRAID", Installer: "own", Command: lintuxInstall + "mergerfs"},
	{Repo: "chicohaager/zimaos-tailscale-sysext", Title: "Tailscale", Installer: "own", Command: lintuxInstall + "tailscale", ReleaseKey: "SYSEXT_VERSION_ID"},
}

// protected modules are never uninstalled from the catalog: the built-ins
// belong to the Built-in apps tab, modmanager would remove itself mid-request.
var protected = map[string]bool{
	"modmanager": true, "icewhale_files": true, "icewhale_peerdrop": true, "icewhale_photos": true,
	"zimaos_zvm": true, "doconverter": true, "zimanet": true,
}

// Entry is one installable module as the UI shows it.
type Entry struct {
	ID        string `json:"id"`     // "gh:owner/repo"
	Source    string `json:"source"` // "lintux" or "repo"
	Name      string `json:"name,omitempty"`
	Title     string `json:"title"`
	Repo      string `json:"repo,omitempty"`
	Version   string `json:"version,omitempty"`
	Asset     string `json:"asset,omitempty"`
	Installer string `json:"installer"` // "zpkg", "own", "none"
	Command   string `json:"command,omitempty"`
	Installed bool   `json:"installed"`
	// State: "", "current" (installed = release), "update" (installed is
	// older, or — without a readable version — a different image), "newer"
	// (installed version above the release, e.g. a development build),
	// "installed" (nothing to compare).
	State string `json:"state,omitempty"`
	// InstalledVersion is read from the running module, empty when unknown.
	InstalledVersion string `json:"installed_version,omitempty"`
	// RestartNeeded: the module's health route reports "binary":"replaced" — its
	// file is newer than the running process (after `zpkg install --force`, which
	// does not restart the service; lintux-modkit webmod, measured on 1.8.0). A
	// reinstall here goes through zpkg remove + install, which restarts it.
	RestartNeeded bool `json:"restart_needed,omitempty"`
	// StaleUnits: the module's services whose process runs other bytes than the
	// program file its ExecStart names — measured here, so it works for every
	// module, also ones built before their health route reported it.
	StaleUnits []string `json:"stale_units,omitempty"`
	Protected  bool     `json:"protected,omitempty"`
	Error      string   `json:"error,omitempty"`
	Note       string   `json:"note,omitempty"`       // e.g. served from cache because of GitHub's rate limit
	ErrorCode  string   `json:"error_code,omitempty"` // rate_limit | no_release | unreachable | no_image | no_checksum | failed
	StaleAt    string   `json:"stale_at,omitempty"`   // RFC 3339: the release info is this old (rate limit); the UI words it
}

// Catalog lists and installs modules.
type Catalog struct {
	Run       Runner
	Stream    progress.Stream // nil: commands that change the host go through Run, output after the end
	Progress  *progress.Log   // nil: no progress report
	HTTP      *http.Client
	GitHubAPI string // https://api.github.com
	GitHubWeb string // https://github.com — release pages, used when the API is rate limited
	Gateway   string // http://127.0.0.1 — modules' health routes
	StateDir  string // state.Dir, root only
	ExtDir    string // /var/lib/extensions
	UsrLib    string // /usr/lib — where merged extension-release files live
	Root      string // / — the host tree an image's files are checked against
	Arch      string // runtime.GOARCH
	MaxBytes  int64

	mu    sync.Mutex
	cache map[string]cached
	sums  map[string]cachedSum // checksum file URL -> content
	good  map[string]good      // nil until loaded from releaseCacheFile
}

type cachedSum struct {
	at   time.Time
	body []byte
}

type cached struct {
	at    time.Time
	rel   *release
	err   error
	stale time.Time // non-zero: rel is an older answer, served because GitHub refused
}

// good is the last successful answer per repository, kept on disk so a
// restart does not spend the 60 anonymous API calls per hour again.
type good struct {
	At  time.Time `json:"at"`
	Rel *release  `json:"release"`
}

// New returns a Catalog on the host.
func New(run Runner) *Catalog {
	return &Catalog{
		Run:       run,
		HTTP:      &http.Client{Timeout: 10 * time.Minute},
		GitHubAPI: "https://api.github.com",
		GitHubWeb: "https://github.com",
		Gateway:   "http://127.0.0.1",
		StateDir:  state.Dir,
		ExtDir:    "/var/lib/extensions",
		UsrLib:    "/usr/lib",
		Root:      "/",
		Arch:      runtime.GOARCH,
		MaxBytes:  1 << 30,
	}
}

// Errors the API maps to status codes.
var (
	ErrBadRepo      = errors.New("not a GitHub repository (owner/repo or https://github.com/owner/repo)")
	ErrNoImage      = errors.New("the latest release has no sysext image (.raw) for this CPU")
	ErrNoChecksum   = errors.New("the latest release ships no checksum for its image; not installed")
	ErrChecksum     = errors.New("checksum mismatch; not installed")
	ErrOwn          = errors.New("this module needs its own installer")
	ErrProtected    = errors.New("this module is managed by ZimaOS or by Module Manager itself and cannot be changed here")
	ErrUnknown      = errors.New("unknown catalog entry")
	ErrNotInstalled = errors.New("module is not installed")
	ErrRateLimit    = errors.New("GitHub rate limit reached (60 requests per hour without login); try again later")
	ErrNoRelease    = errors.New("no such repository or no release")
	ErrUnreachable  = errors.New("GitHub not reachable")
	ErrCollision    = errors.New("the image would replace files ZimaOS already has; not installed")
)

var repoPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
var moduleName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// errorCode is the code the UI translates for an entry's error.
func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrRateLimit):
		return "rate_limit"
	case errors.Is(err, ErrNoRelease):
		return "no_release"
	case errors.Is(err, ErrUnreachable):
		return "unreachable"
	case errors.Is(err, ErrNoImage):
		return "no_image"
	case errors.Is(err, ErrNoChecksum):
		return "no_checksum"
	}
	return "failed"
}

// ParseRepo accepts owner/repo, github.com/owner/repo and https URLs below it
// (…/releases, .git) and returns "owner/repo".
func ParseRepo(in string) (string, error) {
	s := strings.TrimSpace(in)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "www.")
	s = strings.TrimPrefix(s, "github.com/")
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 {
		return "", ErrBadRepo
	}
	owner, repo := parts[0], strings.TrimSuffix(parts[1], ".git")
	if !repoPart.MatchString(owner) || !repoPart.MatchString(repo) || strings.Contains(owner+repo, "..") {
		return "", ErrBadRepo
	}
	if len(parts) > 2 && parts[2] != "releases" && parts[2] != "tree" && parts[2] != "blob" {
		return "", ErrBadRepo
	}
	return owner + "/" + repo, nil
}

// --- user repositories ---

type sources struct {
	Repos []string `json:"repos"`
}

func (c *Catalog) sourcesFile() string { return filepath.Join(c.StateDir, "sources.json") }

func (c *Catalog) loadSources() (sources, error) {
	var s sources
	b, err := os.ReadFile(c.sourcesFile())
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %w", c.sourcesFile(), err)
	}
	return s, nil
}

func (c *Catalog) saveSources(s sources) error {
	if err := os.MkdirAll(c.StateDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.sourcesFile() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.sourcesFile())
}

func isPreset(repo string) bool {
	for _, p := range Presets {
		if strings.EqualFold(p.Repo, repo) {
			return true
		}
	}
	return false
}

// AddRepo validates the repository against GitHub (it must have a release
// with an image) and remembers it. Returns the normalised owner/repo.
func (c *Catalog) AddRepo(ctx context.Context, in string) (string, error) {
	repo, err := ParseRepo(in)
	if err != nil {
		return "", err
	}
	rel, _, err := c.latest(ctx, repo, true)
	if err != nil {
		return "", err
	}
	if _, _, err := c.pick(rel); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.loadSources()
	if err != nil {
		return "", err
	}
	if isPreset(repo) {
		return repo, nil
	}
	for _, r := range s.Repos {
		if strings.EqualFold(r, repo) {
			return r, nil
		}
	}
	s.Repos = append(s.Repos, repo)
	return repo, c.saveSources(s)
}

// RemoveRepo forgets a user repository (an installed module stays installed).
func (c *Catalog) RemoveRepo(in string) error {
	repo, err := ParseRepo(in)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.loadSources()
	if err != nil {
		return err
	}
	out := s.Repos[:0]
	for _, r := range s.Repos {
		if !strings.EqualFold(r, repo) {
			out = append(out, r)
		}
	}
	s.Repos = out
	return c.saveSources(s)
}

// --- GitHub releases ---

type release struct {
	Tag    string  `json:"tag_name"`
	Assets []asset `json:"assets"`
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

const releaseTTL = 10 * time.Minute

func (c *Catalog) releaseCacheFile() string { return filepath.Join(c.StateDir, "release-cache.json") }

// loadGood reads the on-disk cache once; c.mu must be held.
func (c *Catalog) loadGood() {
	if c.good != nil {
		return
	}
	c.good = map[string]good{}
	if b, err := os.ReadFile(c.releaseCacheFile()); err == nil {
		_ = json.Unmarshal(b, &c.good) // a broken cache is only a cache
	}
}

// saveGood writes the on-disk cache; c.mu must be held.
func (c *Catalog) saveGood() {
	b, err := json.Marshal(c.good)
	if err != nil {
		return
	}
	if err := os.MkdirAll(c.StateDir, 0o755); err != nil {
		return
	}
	tmp := c.releaseCacheFile() + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, c.releaseCacheFile())
	}
}

// latest reads the newest release. Answers are cached for ten minutes, in
// memory and on disk, to stay inside GitHub's 60 anonymous API calls per
// hour (measured 2026-10-21: a day of testing from one LAN address used them
// up). When GitHub refuses with its rate limit, the last good answer is
// served and its age returned as stale.
func (c *Catalog) latest(ctx context.Context, repo string, fresh bool) (*release, time.Time, error) {
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]cached{}
	}
	c.loadGood()
	if e, ok := c.cache[repo]; ok && !fresh && time.Since(e.at) < releaseTTL {
		c.mu.Unlock()
		return e.rel, e.stale, e.err
	}
	if g, ok := c.good[repo]; ok && !fresh && time.Since(g.At) < releaseTTL && g.Rel != nil {
		c.mu.Unlock()
		return g.Rel, time.Time{}, nil
	}
	c.mu.Unlock()
	rel, err := c.fetchLatest(ctx, repo)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		c.cache[repo] = cached{at: time.Now(), rel: rel}
		c.good[repo] = good{At: time.Now(), Rel: rel}
		c.saveGood()
		return rel, time.Time{}, nil
	}
	if g, ok := c.good[repo]; ok && errors.Is(err, ErrRateLimit) && g.Rel != nil {
		c.cache[repo] = cached{at: time.Now(), rel: g.Rel, stale: g.At}
		return g.Rel, g.At, nil
	}
	if ctx.Err() != nil {
		// canceled (a closed tab) or out of time: that says nothing about GitHub
		return nil, time.Time{}, err
	}
	c.cache[repo] = cached{at: time.Now(), err: err}
	return nil, time.Time{}, err
}

func (c *Catalog) fetchLatest(ctx context.Context, repo string) (*release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.GitHubAPI+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%s: %w", repo, ErrNoRelease)
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		// The anonymous API allows 60 calls per hour per address (measured 2026-10-23: used up on a
		// test day). github.com's release pages do not count against it — ask them before giving up.
		if rel, werr := c.webLatest(ctx, repo); werr == nil {
			return rel, nil
		}
		return nil, ErrRateLimit
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub answered %s for %s", resp.Status, repo)
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("GitHub answer for %s: %w", repo, err)
	}
	return &rel, nil
}

// webLatest reads the newest release from github.com's pages instead of the API:
// /<repo>/releases/latest redirects to /<repo>/releases/tag/<tag>, and
// /<repo>/releases/expanded_assets/<tag> lists the assets as download links.
// Measured 2026-10-23: neither counts against the API's rate limit.
func (c *Catalog) webLatest(ctx context.Context, repo string) (*release, error) {
	if c.GitHubWeb == "" {
		return nil, errors.New("no release pages configured")
	}
	noFollow := *c.HTTP
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.GitHubWeb+"/"+repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		return nil, err
	}
	_ = resp.Body.Close()
	loc, err := resp.Location()
	if err != nil {
		return nil, fmt.Errorf("%s: release page did not redirect to a tag (%s)", repo, resp.Status)
	}
	prefix := "/" + repo + "/releases/tag/"
	if !strings.HasPrefix(loc.Path, prefix) || len(loc.Path) == len(prefix) {
		return nil, fmt.Errorf("%s: unexpected release redirect %s", repo, loc.Path)
	}
	tag := strings.TrimPrefix(loc.Path, prefix)
	page, err := c.get(ctx, c.GitHubWeb+"/"+repo+"/releases/expanded_assets/"+url.PathEscape(tag), 2<<20)
	if err != nil {
		return nil, err
	}
	link := regexp.MustCompile(`href="(/` + regexp.QuoteMeta(repo) + `/releases/download/` + regexp.QuoteMeta(url.PathEscape(tag)) + `/([^"/]+))"`)
	rel := &release{Tag: tag}
	seen := map[string]bool{}
	for _, m := range link.FindAllStringSubmatch(string(page), -1) {
		name, err := url.PathUnescape(m[2])
		if err != nil || seen[name] {
			continue
		}
		seen[name] = true
		rel.Assets = append(rel.Assets, asset{Name: name, URL: c.GitHubWeb + m[1]})
	}
	if len(rel.Assets) == 0 {
		return nil, fmt.Errorf("%s %s: no assets on the release page", repo, tag)
	}
	return rel, nil
}

// archNames are the spellings of this CPU in asset names.
func archNames(goarch string) []string {
	switch goarch {
	case "amd64":
		return []string{"amd64", "x86_64", "x86-64"}
	case "arm64":
		return []string{"arm64", "aarch64"}
	}
	return []string{goarch}
}

var otherArch = regexp.MustCompile(`[-_](amd64|x86_64|x86-64|arm64|aarch64|armhf|armv7)\.raw$`)

// pick chooses the image for this CPU and its checksum: <name>[-_]<arch>.raw
// first, else a single arch-less <name>.raw — but only in a release without any
// arch-specific image: lintux-modkit's build-sysext.sh writes <name>.raw for amd64
// as a copy of <name>-amd64.raw, so next to one it is the amd64 image, and an arm64
// host would install it. Returns the module name, the image asset and the expected
// sha256 (empty when the release has none).
func (c *Catalog) pick(rel *release) (string, asset, error) {
	var chosen *asset
	var name string
	for _, a := range archNames(c.Arch) {
		for i := range rel.Assets {
			n := rel.Assets[i].Name
			for _, sep := range []string{"-", "_"} {
				if strings.HasSuffix(n, sep+a+".raw") {
					chosen, name = &rel.Assets[i], strings.TrimSuffix(n, sep+a+".raw")
					break
				}
			}
			if chosen != nil {
				break
			}
		}
		if chosen != nil {
			break
		}
	}
	if chosen == nil {
		var plain []*asset
		archSpecific := false
		for i := range rel.Assets {
			n := rel.Assets[i].Name
			if otherArch.MatchString(n) {
				archSpecific = true
			} else if strings.HasSuffix(n, ".raw") {
				plain = append(plain, &rel.Assets[i])
			}
		}
		if len(plain) == 1 && !archSpecific {
			chosen, name = plain[0], strings.TrimSuffix(plain[0].Name, ".raw")
		}
	}
	if chosen == nil || !moduleName.MatchString(name) {
		return "", asset{}, ErrNoImage
	}
	return name, *chosen, nil
}

// checksumFor finds the expected sha256 of an image in the release:
// <image>.sha256 ("<hex>  <file>" or just "<hex>") or a SHA256SUMS line.
func (c *Catalog) checksumFor(ctx context.Context, rel *release, img asset) (string, error) {
	var sumURL, list string
	for _, a := range rel.Assets {
		switch a.Name {
		case img.Name + ".sha256":
			sumURL = a.URL
		case "SHA256SUMS", "SHA256SUMS.txt", "sha256sums.txt":
			list = a.URL
		}
	}
	if sumURL == "" && list == "" {
		return "", ErrNoChecksum
	}
	u := sumURL
	if u == "" {
		u = list
	}
	b, err := c.sumFile(ctx, u)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		file := ""
		if len(f) > 1 {
			file = strings.TrimPrefix(f[1], "*")
		}
		if sumURL != "" && (file == "" || file == img.Name) || sumURL == "" && file == img.Name {
			h := strings.ToLower(f[0])
			if len(h) == 64 && isHex(h) {
				return h, nil
			}
		}
	}
	return "", ErrNoChecksum
}

// sumFile fetches a checksum file, cached for ten minutes: the catalog list
// checks every entry on each load.
func (c *Catalog) sumFile(ctx context.Context, u string) ([]byte, error) {
	c.mu.Lock()
	if c.sums == nil {
		c.sums = map[string]cachedSum{}
	}
	if e, ok := c.sums[u]; ok && time.Since(e.at) < 10*time.Minute {
		c.mu.Unlock()
		return e.body, nil
	}
	c.mu.Unlock()
	b, err := c.get(ctx, u, 1<<20)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.sums[u] = cachedSum{at: time.Now(), body: b}
	c.mu.Unlock()
	return b, nil
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

func (c *Catalog) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	if err := allowedURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", u, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// AllowedHosts are where release assets may come from; tests add theirs.
var AllowedHosts = map[string]bool{"github.com": true}

func allowedURL(u string) error {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return fmt.Errorf("invalid download URL %q", u)
	}
	if !AllowedHosts[p.Host] || (p.Scheme != "https" && !AllowedHosts["http:"+p.Host]) {
		return fmt.Errorf("download host %s is not allowed", p.Host)
	}
	return nil
}

// --- list ---

// List returns the presets and the repositories the user added.
func (c *Catalog) List(ctx context.Context) ([]Entry, []string, error) {
	c.mu.Lock()
	s, err := c.loadSources()
	c.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	var out []Entry
	for _, p := range Presets {
		out = append(out, c.repoEntry(ctx, p.Repo, "lintux", p))
	}
	for _, r := range s.Repos {
		out = append(out, c.repoEntry(ctx, r, "repo", Preset{Repo: r, Installer: "zpkg"}))
	}
	if s.Repos == nil {
		s.Repos = []string{}
	}
	return out, s.Repos, nil
}

func (c *Catalog) installedFile(name string) string { return filepath.Join(c.ExtDir, name+".raw") }

func (c *Catalog) isInstalled(name string) bool {
	_, err := os.Lstat(c.installedFile(name))
	return err == nil
}

func (c *Catalog) repoEntry(ctx context.Context, repo, source string, p Preset) Entry {
	e := Entry{ID: "gh:" + repo, Source: source, Repo: repo, Title: p.Title, Installer: p.Installer, Command: p.Command}
	if e.Title == "" {
		e.Title = repo[strings.Index(repo, "/")+1:]
	}
	rel, stale, err := c.latest(ctx, repo, false)
	if err != nil {
		e.Error, e.ErrorCode = err.Error(), errorCode(err)
		return e
	}
	if !stale.IsZero() {
		e.Note = "release info from " + stale.Local().Format("2006-01-02 15:04") + " — GitHub rate limit, newer releases not checked"
		e.StaleAt = stale.Format(time.RFC3339)
	}
	e.Version = rel.Tag
	name, img, err := c.pick(rel)
	if err != nil {
		if p.Installer != "own" {
			e.Error, e.ErrorCode = err.Error(), errorCode(err)
			return e
		}
		// An own installer without an image (zfw ships a tarball): the
		// health route alone tells whether and which version runs.
		if v, replaced := c.installedVersion(ctx, "", p); v != "" {
			e.Installed, e.InstalledVersion, e.RestartNeeded = true, v, replaced
			e.State = stateFor(v, rel.Tag)
		}
		return e
	}
	e.Name, e.Asset = name, img.Name
	e.Protected = protected[name]
	e.Installed = c.isInstalled(name)
	want, sumErr := c.checksumFor(ctx, rel, img)
	if errors.Is(sumErr, ErrNoChecksum) && p.Installer != "own" {
		e.Error, e.ErrorCode = "the release ships no checksum for "+img.Name+"; it cannot be installed from here", "no_checksum"
	}
	if e.Installed {
		if units := c.staleUnits(ctx, name); len(units) > 0 {
			e.StaleUnits, e.RestartNeeded = units, true
		}
		if v, replaced := c.installedVersion(ctx, name, p); v != "" {
			e.InstalledVersion, e.RestartNeeded = v, e.RestartNeeded || replaced
			e.State = stateFor(v, rel.Tag)
			return e
		}
	}
	if e.Installed {
		e.State = "installed"
		if sumErr == nil {
			if got, err := fileSHA256(c.installedFile(name)); err == nil {
				if got == want {
					e.State = "current"
				} else {
					e.State = "update"
				}
			}
		}
	}
	return e
}

// --- install / uninstall ---

// Install installs (or updates) one entry.
func (c *Catalog) Install(ctx context.Context, id string) error {
	switch {
	case strings.HasPrefix(id, "gh:"):
		repo, err := ParseRepo(strings.TrimPrefix(id, "gh:"))
		if err != nil {
			return err
		}
		if !c.knownRepo(repo) {
			return ErrUnknown
		}
		return c.installRepo(ctx, repo)
	}
	return ErrUnknown
}

func (c *Catalog) knownRepo(repo string) bool {
	if isPreset(repo) {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.loadSources()
	if err != nil {
		return false
	}
	for _, r := range s.Repos {
		if strings.EqualFold(r, repo) {
			return true
		}
	}
	return false
}

func (c *Catalog) installRepo(ctx context.Context, repo string) error {
	for _, p := range Presets {
		if strings.EqualFold(p.Repo, repo) && p.Installer == "own" {
			return fmt.Errorf("%w: %s", ErrOwn, p.Command)
		}
	}
	p := c.Progress
	p.Stepf("release", "Asking GitHub for the latest release of {repo}", "repo", repo)
	rel, _, err := c.latest(ctx, repo, true)
	if err != nil {
		return err
	}
	name, img, err := c.pick(rel)
	if err != nil {
		return err
	}
	if protected[name] {
		return ErrProtected
	}
	p.Stepf("image", "Release {tag}: image {file}", "tag", rel.Tag, "file", img.Name)
	want, err := c.checksumFor(ctx, rel, img)
	if err != nil {
		return err
	}
	dir := filepath.Join(c.StateDir, "downloads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// zpkg matches the file name against the module name.
	dst := filepath.Join(dir, name+".raw")
	defer func() { _ = os.Remove(dst) }()
	if err := c.download(ctx, img.URL, dst, want); err != nil {
		return err
	}
	if err := c.checkCollisions(ctx, name, dst); err != nil {
		return err
	}
	var previous string
	if c.isInstalled(name) {
		// zpkg refuses a second image of the same name, so the installed one is
		// removed first; a copy (as zpkg wants it: <name>.raw) is the way back.
		previous = filepath.Join(dir, "previous", name+".raw")
		if err := os.MkdirAll(filepath.Dir(previous), 0o700); err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(filepath.Dir(previous)) }()
		p.Stepf("keep", "Keeping a copy of the installed image for the way back")
		if err := copyFile(c.installedFile(name), previous); err != nil {
			return fmt.Errorf("no copy of the installed image for the way back: %w", err)
		}
		if _, err := c.change(ctx, "zpkg", "remove", name); err != nil {
			return err
		}
	}
	if out, err := c.change(ctx, "zpkg", "install", dst); err != nil {
		if previous == "" {
			return err
		}
		failed := fmt.Errorf("zpkg install: %v %s", err, strings.TrimSpace(string(out)))
		p.Errf("wayBack", "zpkg install failed; installing the previous version again")
		// the way back also when the request was canceled: the module must not stay removed
		bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if _, rerr := c.change(bctx, "zpkg", "install", previous); rerr != nil {
			return fmt.Errorf("%w; the previous version could not be installed again: %v", failed, rerr)
		}
		_, _ = c.change(bctx, "systemctl", "daemon-reload")
		return fmt.Errorf("%w; the previous version installed again", failed)
	}
	_, err = c.change(ctx, "systemctl", "daemon-reload")
	return err
}

// change runs a command that changes the host and reports it with its output.
func (c *Catalog) change(ctx context.Context, name string, args ...string) ([]byte, error) {
	return c.Progress.Exec(ctx, c.Stream, c.Run, name, args...)
}

// checkCollisions refuses an image that carries a file the host already has.
// A sysext overlays /usr, so such a file would replace the host's own for every
// service — measured on ZimaOS 1.8.0 (2026-10-23): an image with /usr/bin/cmp
// would have shadowed ZimaOS' BusyBox cmp. On an update the files of the
// installed version of the same module are in /usr already and do not count.
// Without a file list there is no check, so that refuses the install too.
func (c *Catalog) checkCollisions(ctx context.Context, name, img string) error {
	c.Progress.Stepf("collisions", "Checking the image's files against ZimaOS")
	files, err := c.imageFiles(ctx, img)
	if err != nil {
		return err
	}
	own := map[string]bool{}
	if c.isInstalled(name) {
		old, err := c.imageFiles(ctx, c.installedFile(name))
		if err != nil {
			return err
		}
		for _, f := range old {
			own[f] = true
		}
	}
	var hit []string
	for _, f := range files {
		if own[f] || strings.HasPrefix(f, "/usr/lib/extension-release.d/") {
			continue
		}
		fi, err := os.Lstat(filepath.Join(c.Root, f))
		if err != nil || fi.IsDir() {
			continue
		}
		hit = append(hit, f)
	}
	if len(hit) == 0 {
		c.Progress.Okf("noCollision", "{n} files, none replaces a file of ZimaOS", "n", fmt.Sprint(len(files)))
		return nil
	}
	if len(hit) > 5 {
		hit = append(hit[:5], fmt.Sprintf("and %d more", len(hit)-5))
	}
	return fmt.Errorf("%w: %s", ErrCollision, strings.Join(hit, ", "))
}

// imageFiles lists the paths in a squashfs image with the host's unsquashfs
// (present on ZimaOS 1.8.0), as absolute paths: "squashfs-root/usr/bin/x" -> "/usr/bin/x".
func (c *Catalog) imageFiles(ctx context.Context, img string) ([]string, error) {
	out, err := c.Run(ctx, "unsquashfs", "-lc", img)
	if err != nil {
		return nil, fmt.Errorf("list the image's files (unsquashfs): %w", err)
	}
	var files []string
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "squashfs-root/"); ok && rest != "" {
			files = append(files, "/"+rest)
		}
	}
	return files, nil
}

func (c *Catalog) download(ctx context.Context, u, dst, want string) error {
	if err := allowedURL(u); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", u, resp.Status)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	c.Progress.Stepf("download", "Downloading {url}", "url", u)
	bar := &barWriter{log: c.Progress, total: resp.ContentLength}
	n, err := io.Copy(io.MultiWriter(f, h, bar), io.LimitReader(resp.Body, c.MaxBytes+1))
	bar.done()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > c.MaxBytes {
		return fmt.Errorf("image larger than %d bytes; not installed", c.MaxBytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%w (expected %s, got %s)", ErrChecksum, want[:12], got[:12])
	}
	c.Progress.Okf("checksum", "sha256 {sum}… matches the release's checksum", "sum", want[:12])
	return nil
}

// barWriter reports a download's progress at most four times a second.
type barWriter struct {
	log   *progress.Log
	total int64
	n     int64
	last  time.Time
}

func (b *barWriter) Write(p []byte) (int, error) {
	b.n += int64(len(p))
	if time.Since(b.last) >= 250*time.Millisecond {
		b.last = time.Now()
		b.log.Download(b.n, b.total)
	}
	return len(p), nil
}

func (b *barWriter) done() { b.log.Download(b.n, b.total) }

// Uninstall removes an added module with zpkg (its data under
// /DATA/AppData stays, as lintux' installer relies on).
func (c *Catalog) Uninstall(ctx context.Context, name string) error {
	if !moduleName.MatchString(name) {
		return ErrUnknown
	}
	if protected[name] {
		return ErrProtected
	}
	if !c.isInstalled(name) {
		return ErrNotInstalled
	}
	if _, err := c.change(ctx, "zpkg", "remove", name); err != nil {
		return err
	}
	_, err := c.change(ctx, "systemctl", "daemon-reload")
	return err
}

// staleUnits lists the services of an installed module that run an older
// program than the merged image holds: its unit files are in the image
// (/usr/lib/systemd/system/*.service; a template counts with its running
// instances), stale.Find compares /proc/<pid>/exe with the ExecStart file.
func (c *Catalog) staleUnits(ctx context.Context, name string) []string {
	files, err := c.imageFiles(ctx, c.installedFile(name))
	if err != nil {
		return nil
	}
	sysctl := func(ctx context.Context, args ...string) ([]byte, error) { return c.Run(ctx, "systemctl", args...) }
	var units []string
	for _, f := range files {
		dir, base := path.Split(f)
		if dir != "/usr/lib/systemd/system/" || !strings.HasSuffix(base, ".service") {
			continue
		}
		if tmpl, ok := strings.CutSuffix(base, "@.service"); ok {
			out, err := sysctl(ctx, "list-units", "--plain", "--no-legend", "--state=running", tmpl+"@*.service")
			if err != nil {
				continue
			}
			for _, l := range strings.Split(string(out), "\n") {
				if u := strings.Fields(l); len(u) > 0 && strings.HasPrefix(u[0], tmpl+"@") {
					units = append(units, u[0])
				}
			}
			continue
		}
		units = append(units, base)
	}
	if len(units) == 0 {
		return nil
	}
	return stale.Find(ctx, stale.Options{Units: units, Proc: filepath.Join(c.Root, "proc"), Systemctl: sysctl})
}

// installedVersion asks the running module for its version (health route)
// and falls back to its merged extension-release — SYSEXT_VERSION_ID unless
// the preset names another key; "" when neither answers. replaced: the health
// route says the running process is older than its file.
func (c *Catalog) installedVersion(ctx context.Context, name string, p Preset) (string, bool) {
	if v, replaced := c.healthVersion(ctx, p); v != "" {
		return v, replaced
	}
	if name == "" {
		return "", false
	}
	key := p.ReleaseKey
	if key == "" {
		key = "SYSEXT_VERSION_ID"
	}
	b, err := os.ReadFile(filepath.Join(c.UsrLib, "extension-release.d", "extension-release."+name))
	if err != nil {
		return "", false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return cleanVersion(strings.Trim(v, `"'`)), false
		}
	}
	return "", false
}

func (c *Catalog) healthVersion(ctx context.Context, p Preset) (string, bool) {
	if p.HealthPath == "" {
		return "", false
	}
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, c.Gateway+p.HealthPath, nil)
	if err != nil {
		return "", false
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var h struct {
		Version string `json:"version"`
		Binary  string `json:"binary"` // lintux-modkit webmod: current, replaced, unknown
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&h) != nil {
		return "", false
	}
	v := cleanVersion(h.Version)
	return v, v != "" && h.Binary == "replaced"
}

// stateFor compares an installed version with the release tag.
func stateFor(installed, tag string) string {
	switch cmp := CompareVersions(installed, tag); {
	case cmp == 0:
		return "current"
	case cmp < 0:
		return "update"
	}
	return "newer"
}

var versionShape = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+){0,2}(-[0-9A-Za-z.-]+)?$`)

func cleanVersion(v string) string {
	v = strings.TrimSpace(v)
	if !versionShape.MatchString(v) {
		return ""
	}
	return strings.TrimPrefix(v, "v")
}

// CompareVersions compares two versions semver-style: numeric parts first,
// and a pre-release ("0.3.0-dev7") sorts before its release ("0.3.0"), as
// lintux-modkit's installer does (tools/test-semver.sh). A leading "v" is
// ignored. Returns -1, 0 or 1.
func CompareVersions(a, b string) int {
	a, b = strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v")
	a, _, _ = strings.Cut(a, "+") // build metadata does not order (semver §10)
	b, _, _ = strings.Cut(b, "+")
	ac, apre, _ := strings.Cut(a, "-")
	bc, bpre, _ := strings.Cut(b, "-")
	as, bs := strings.Split(ac, "."), strings.Split(bc, ".")
	for i := 0; i < 3; i++ {
		x, y := numAt(as, i), numAt(bs, i)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case apre == bpre:
		return 0
	case apre == "":
		return 1
	case bpre == "":
		return -1
	}
	return comparePre(apre, bpre)
}

func numAt(p []string, i int) int {
	if i >= len(p) {
		return 0
	}
	n := 0
	for _, r := range p[i] {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// comparePre orders pre-release identifiers dot by dot; a trailing number in
// an identifier compares numerically, so dev10 sorts after dev7.
func comparePre(a, b string) int {
	ap, bp := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		as, an := splitNum(ap[i])
		bs, bn := splitNum(bp[i])
		if as != bs {
			if as < bs {
				return -1
			}
			return 1
		}
		if an != bn {
			if an < bn {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ap) < len(bp):
		return -1
	case len(ap) > len(bp):
		return 1
	}
	return 0
}

func splitNum(s string) (string, int) {
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	n := 0
	for _, r := range s[i:] {
		n = n*10 + int(r-'0')
	}
	return s[:i], n
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
