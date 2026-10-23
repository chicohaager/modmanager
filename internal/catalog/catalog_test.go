package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub serves /repos/<o>/<r>/releases/latest and the assets it lists,
// plus app-management's module list.
type fakeGitHub struct {
	srv      *httptest.Server
	releases map[string]map[string]string // repo -> asset name -> content
	tags     map[string]string
	store    string
	gotAuth  string
	limited  bool              // answer the release API with 403 like GitHub's rate limit
	health   map[string]string // path -> version answered as {"version": …}
	apiCalls int
	mu       sync.Mutex
}

func newGitHub(t *testing.T) *fakeGitHub {
	g := &fakeGitHub{releases: map[string]map[string]string{}, tags: map[string]string{}, health: map[string]string{}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	u, _ := url.Parse(g.srv.URL)
	AllowedHosts[u.Host] = true
	AllowedHosts["http:"+u.Host] = true
	t.Cleanup(func() { delete(AllowedHosts, u.Host); delete(AllowedHosts, "http:"+u.Host) })
	return g
}

func (g *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if v, ok := g.health[r.URL.Path]; ok {
		_, _ = w.Write([]byte(`{"status":"healthy","version":"` + v + `"}`))
		return
	}
	if r.URL.Path == "/v2/mod_management/management/modules" {
		g.gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(g.store))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/releases/latest") {
		g.apiCalls++
		if g.limited {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/releases/latest")
		assets, ok := g.releases[repo]
		if !ok {
			http.NotFound(w, r)
			return
		}
		var list []map[string]any
		for n := range assets {
			list = append(list, map[string]any{"name": n, "browser_download_url": g.srv.URL + "/dl/" + repo + "/" + n})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": g.tags[repo], "assets": list})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/dl/") {
		rest := strings.TrimPrefix(r.URL.Path, "/dl/")
		i := strings.LastIndex(rest, "/")
		if c, ok := g.releases[rest[:i]][rest[i+1:]]; ok {
			_, _ = w.Write([]byte(c))
			return
		}
	}
	http.NotFound(w, r)
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

type recorder struct {
	mu   sync.Mutex
	cmds []string
	// zpkg install copies the image to ExtDir, as the real one does.
	ext string
	// lists answers `unsquashfs -lc <file>` by the file's content: the image
	// strings in these tests stand for squashfs images.
	lists map[string]string
}

func (r *recorder) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := name + " " + strings.Join(args, " ")
	r.cmds = append(r.cmds, line)
	if name == "unsquashfs" && len(args) == 2 && args[0] == "-lc" {
		b, err := os.ReadFile(args[1])
		if err != nil {
			return nil, fmt.Errorf("unsquashfs: %w", err)
		}
		return []byte("squashfs-root\n" + r.lists[string(b)]), nil
	}
	if name == "zpkg" && len(args) == 2 && args[0] == "install" && strings.HasSuffix(args[1], ".raw") {
		b, err := os.ReadFile(args[1])
		if err != nil {
			return nil, fmt.Errorf("zpkg: %w", err)
		}
		return nil, os.WriteFile(filepath.Join(r.ext, filepath.Base(args[1])), b, 0o644)
	}
	return nil, nil
}

func (r *recorder) has(prefix string) bool {
	for _, c := range r.cmds {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func setup(t *testing.T) (*Catalog, *fakeGitHub, *recorder) {
	t.Helper()
	g := newGitHub(t)
	dir := t.TempDir()
	rec := &recorder{ext: filepath.Join(dir, "ext")}
	if err := os.MkdirAll(rec.ext, 0o755); err != nil {
		t.Fatal(err)
	}
	old := Presets
	Presets = []Preset{
		{Repo: "lintux/cron", Title: "Cron", Installer: "zpkg", HealthPath: "/cron/health"},
		{Repo: "lintux/tailscale", Title: "Tailscale", Installer: "own", Command: "x", ReleaseKey: "SYSEXT_VERSION_ID"},
		{Repo: "lintux/zfw", Title: "ZFW", Installer: "own", Command: "sudo bash install.sh --install zfw", HealthPath: "/v2/zfw/api/health"},
	}
	t.Cleanup(func() { Presets = old })
	c := &Catalog{Run: rec.run, HTTP: g.srv.Client(), GitHubAPI: g.srv.URL, Gateway: g.srv.URL,
		StateDir: filepath.Join(dir, "state"), ExtDir: rec.ext, UsrLib: filepath.Join(dir, "usrlib"), Arch: "amd64", MaxBytes: 1 << 20,
		Root: filepath.Join(dir, "root")}
	return c, g, rec
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestParseRepo(t *testing.T) {
	good := map[string]string{
		"chicohaager/cron":                         "chicohaager/cron",
		"https://github.com/chicohaager/cron":      "chicohaager/cron",
		"github.com/chicohaager/cron.git":          "chicohaager/cron",
		" https://github.com/a-b/c.d_e/releases/ ": "a-b/c.d_e",
		"https://github.com/o/r/tree/main":         "o/r",
	}
	for in, want := range good {
		got, err := ParseRepo(in)
		if err != nil || got != want {
			t.Errorf("ParseRepo(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "cron", "https://evil.example/o/r", "o/r/../../x", "../r", "o/r;rm", "o/r/issues", "https://github.com/o", "a..b/r"} {
		if got, err := ParseRepo(in); err == nil {
			t.Errorf("ParseRepo(%q) = %q, want error", in, got)
		}
	}
}

func TestPickPrefersThisCPUAndRefusesForeignOnly(t *testing.T) {
	c := &Catalog{Arch: "amd64"}
	cases := []struct {
		assets []string
		name   string
		asset  string
		err    error
	}{
		{[]string{"cron-amd64.raw", "cron-arm64.raw", "cron.raw"}, "cron", "cron-amd64.raw", nil},
		{[]string{"mergerfs-snapraid_amd64.raw", "mergerfs-snapraid_arm64.raw", "SHA256SUMS"}, "mergerfs-snapraid", "mergerfs-snapraid_amd64.raw", nil},
		{[]string{"web-ftp-client.raw", "web-ftp-client.raw.sha256"}, "web-ftp-client", "web-ftp-client.raw", nil},
		{[]string{"x-arm64.raw"}, "", "", ErrNoImage},
		{[]string{"a.raw", "b.raw"}, "", "", ErrNoImage},
		{[]string{"tool-1.0-amd64.tar.gz"}, "", "", ErrNoImage},
		{[]string{"Bad Name-amd64.raw"}, "", "", ErrNoImage},
	}
	for _, tc := range cases {
		rel := &release{}
		for _, a := range tc.assets {
			rel.Assets = append(rel.Assets, asset{Name: a})
		}
		name, a, err := c.pick(rel)
		if !errors.Is(err, tc.err) || name != tc.name || a.Name != tc.asset {
			t.Errorf("%v: got %q %q %v, want %q %q %v", tc.assets, name, a.Name, err, tc.name, tc.asset, tc.err)
		}
	}
}

func TestInstallVerifiesRenamesAndInstalls(t *testing.T) {
	c, g, rec := setup(t)
	img := "squashfs-cron-v2"
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": img, "cron-amd64.raw.sha256": sum(img) + "  cron-amd64.raw\n", "cron-arm64.raw": "other"}
	g.tags["lintux/cron"] = "v0.3.3"
	must(t, os.WriteFile(filepath.Join(rec.ext, "cron.raw"), []byte("squashfs-cron-v1"), 0o644))
	must(t, c.Install(context.Background(), "gh:lintux/cron"))
	dl := filepath.Join(c.StateDir, "downloads", "cron.raw")
	want := []string{"unsquashfs -lc " + dl, "unsquashfs -lc " + filepath.Join(rec.ext, "cron.raw"), "zpkg remove cron", "zpkg install " + dl, "systemctl daemon-reload"}
	if strings.Join(rec.cmds, "|") != strings.Join(want, "|") {
		t.Fatalf("commands %v, want %v", rec.cmds, want)
	}
	if b, _ := os.ReadFile(filepath.Join(rec.ext, "cron.raw")); string(b) != img {
		t.Errorf("installed image %q, want the release image", b)
	}
	if _, err := os.Stat(filepath.Join(c.StateDir, "downloads", "cron.raw")); !os.IsNotExist(err) {
		t.Errorf("download left behind: %v", err)
	}
	entries, _, err := c.List(context.Background())
	must(t, err)
	for _, e := range entries {
		if e.ID == "gh:lintux/cron" && (e.State != "current" || e.Version != "v0.3.3") {
			t.Errorf("after install: %+v, want state current v0.3.3", e)
		}
	}
}

// A sysext overlays /usr: a file at a path the host already has replaces the
// host's own for every service. Measured on ZimaOS 1.8.0 (2026-10-23): an
// admintools image carried /usr/bin/cmp, which ZimaOS ships as a BusyBox applet.
func TestInstallRefusesAnImageThatWouldReplaceHostFiles(t *testing.T) {
	c, g, rec := setup(t)
	img := "squashfs-tools-v1"
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": img, "cron-amd64.raw.sha256": sum(img)}
	rec.lists = map[string]string{img: "squashfs-root/usr\nsquashfs-root/usr/bin\nsquashfs-root/usr/bin/cmp\nsquashfs-root/usr/bin/newtool\n" +
		"squashfs-root/usr/lib/extension-release.d/extension-release.cron\n"}
	must(t, os.MkdirAll(filepath.Join(c.Root, "usr/bin"), 0o755))
	must(t, os.MkdirAll(filepath.Join(c.Root, "usr/lib/extension-release.d"), 0o755))
	must(t, os.WriteFile(filepath.Join(c.Root, "usr/bin/cmp"), []byte("busybox"), 0o755))
	err := c.Install(context.Background(), "gh:lintux/cron")
	if !errors.Is(err, ErrCollision) || !strings.Contains(err.Error(), "/usr/bin/cmp") {
		t.Fatalf("collision not refused: %v", err)
	}
	if strings.Contains(err.Error(), "newtool") || strings.Contains(err.Error(), "/usr/bin,") {
		t.Errorf("error names a file that does not collide: %v", err)
	}
	if rec.has("zpkg") {
		t.Fatalf("zpkg ran despite a collision: %v", rec.cmds)
	}
}

// An update must not collide with the files of the version it replaces.
func TestInstallUpdateIgnoresTheInstalledVersionsOwnFiles(t *testing.T) {
	c, g, rec := setup(t)
	img := "squashfs-tools-v2"
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": img, "cron-amd64.raw.sha256": sum(img)}
	rec.lists = map[string]string{
		img:                 "squashfs-root/usr/bin/newtool\nsquashfs-root/usr/bin/second\n",
		"squashfs-tools-v1": "squashfs-root/usr/bin/newtool\n",
	}
	must(t, os.WriteFile(filepath.Join(rec.ext, "cron.raw"), []byte("squashfs-tools-v1"), 0o644))
	must(t, os.MkdirAll(filepath.Join(c.Root, "usr/bin"), 0o755))
	must(t, os.WriteFile(filepath.Join(c.Root, "usr/bin/newtool"), []byte("from v1, merged"), 0o755))
	must(t, c.Install(context.Background(), "gh:lintux/cron"))
	if !rec.has("zpkg install") {
		t.Fatalf("update not installed: %v", rec.cmds)
	}
}

// Without a file list there is no collision check: refuse instead of installing blind.
func TestInstallRefusesWhenTheImageCannotBeListed(t *testing.T) {
	c, g, rec := setup(t)
	img := "squashfs-x"
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": img, "cron-amd64.raw.sha256": sum(img)}
	run := c.Run
	c.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "unsquashfs" {
			return nil, errors.New("exec: unsquashfs: not found")
		}
		return run(ctx, name, args...)
	}
	if err := c.Install(context.Background(), "gh:lintux/cron"); err == nil || !strings.Contains(err.Error(), "unsquashfs") {
		t.Fatalf("install without a file list: %v", err)
	}
	if rec.has("zpkg") {
		t.Fatalf("zpkg ran without a collision check: %v", rec.cmds)
	}
}

func TestInstallRefusesAWrongOrMissingChecksum(t *testing.T) {
	c, g, rec := setup(t)
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": "tampered", "cron-amd64.raw.sha256": sum("original")}
	err := c.Install(context.Background(), "gh:lintux/cron")
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("wrong checksum: %v", err)
	}
	if rec.has("zpkg") {
		t.Fatalf("zpkg ran despite a checksum mismatch: %v", rec.cmds)
	}
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": "img"}
	c.cache = nil
	if err := c.Install(context.Background(), "gh:lintux/cron"); !errors.Is(err, ErrNoChecksum) {
		t.Fatalf("no checksum: %v", err)
	}
	if rec.has("zpkg") {
		t.Fatalf("zpkg ran without a checksum: %v", rec.cmds)
	}
}

func TestInstallWithSHA256SUMS(t *testing.T) {
	c, g, rec := setup(t)
	img := "mergerfs-image"
	must(t, os.MkdirAll(c.StateDir, 0o755))
	must(t, os.WriteFile(filepath.Join(c.StateDir, "sources.json"), []byte(`{"repos":["x/mergerfs"]}`), 0o644))
	g.releases["x/mergerfs"] = map[string]string{
		"mergerfs-snapraid_amd64.raw": img, "mergerfs-snapraid_arm64.raw": "arm",
		"SHA256SUMS": sum("arm") + "  mergerfs-snapraid_arm64.raw\n" + sum(img) + "  mergerfs-snapraid_amd64.raw\n",
	}
	must(t, c.Install(context.Background(), "gh:x/mergerfs"))
	if !rec.has("zpkg install " + filepath.Join(c.StateDir, "downloads", "mergerfs-snapraid.raw")) {
		t.Fatalf("not installed under the module name: %v", rec.cmds)
	}
}

func TestOwnInstallerAndUnknownRepoAreNotInstalled(t *testing.T) {
	c, g, rec := setup(t)
	g.releases["lintux/zfw"] = map[string]string{"zfw-1.0-amd64.tar.gz": "x"}
	if err := c.Install(context.Background(), "gh:lintux/zfw"); !errors.Is(err, ErrOwn) || !strings.Contains(err.Error(), "--install zfw") {
		t.Fatalf("own installer: %v", err)
	}
	g.releases["stranger/mod"] = map[string]string{"mod.raw": "x", "mod.raw.sha256": sum("x")}
	if err := c.Install(context.Background(), "gh:stranger/mod"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("repo never added: %v", err)
	}
	if rec.has("zpkg") {
		t.Fatalf("zpkg ran: %v", rec.cmds)
	}
}

func TestAddAndRemoveRepo(t *testing.T) {
	c, g, _ := setup(t)
	ctx := context.Background()
	g.releases["me/tool"] = map[string]string{"tool-amd64.raw": "x", "tool-amd64.raw.sha256": sum("x")}
	got, err := c.AddRepo(ctx, "https://github.com/me/tool/releases")
	must(t, err)
	if got != "me/tool" {
		t.Fatalf("added %q", got)
	}
	_, _ = c.AddRepo(ctx, "me/tool")
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": "c", "cron-amd64.raw.sha256": sum("c")}
	if _, err := c.AddRepo(ctx, "lintux/cron"); err != nil { // preset: valid, but not stored again
		t.Fatal(err)
	}
	_, repos, err := c.List(ctx)
	must(t, err)
	if strings.Join(repos, ",") != "me/tool" {
		t.Fatalf("stored repos %v, want [me/tool]", repos)
	}
	g.releases["me/docs"] = map[string]string{"README.md": "x"}
	if _, err := c.AddRepo(ctx, "me/docs"); !errors.Is(err, ErrNoImage) {
		t.Fatalf("repo without image: %v", err)
	}
	if _, err := c.AddRepo(ctx, "me/missing"); err == nil {
		t.Fatal("repo without release accepted")
	}
	must(t, c.RemoveRepo("me/tool"))
	_, repos, _ = c.List(ctx)
	if len(repos) != 0 {
		t.Fatalf("repos after remove: %v", repos)
	}
}

func TestStateUpdateWhenTheInstalledImageDiffers(t *testing.T) {
	c, g, rec := setup(t)
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": "new", "cron-amd64.raw.sha256": sum("new")}
	must(t, os.WriteFile(filepath.Join(rec.ext, "cron.raw"), []byte("old"), 0o644))
	entries, _, err := c.List(context.Background())
	must(t, err)
	for _, e := range entries {
		if e.ID == "gh:lintux/cron" && (!e.Installed || e.State != "update") {
			t.Fatalf("got %+v, want installed with state update", e)
		}
	}
}

func TestModStoreIsGone(t *testing.T) {
	c, g, rec := setup(t)
	g.store = `{"data":[{"name":"undelit","title":"Undelit"}]}`
	entries, _, err := c.List(context.Background())
	must(t, err)
	for _, e := range entries {
		if e.Source != "lintux" && e.Source != "repo" {
			t.Fatalf("entry from another source: %+v", e)
		}
	}
	if err := c.Install(context.Background(), "store:undelit"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("store install: %v, want ErrUnknown", err)
	}
	if rec.has("zpkg") || g.gotAuth != "" {
		t.Fatalf("store touched: cmds %v, app-management asked %v", rec.cmds, g.gotAuth != "")
	}
}

func TestUninstallGuards(t *testing.T) {
	c, _, rec := setup(t)
	ctx := context.Background()
	if err := c.Uninstall(ctx, "modmanager"); !errors.Is(err, ErrProtected) {
		t.Fatalf("self uninstall: %v", err)
	}
	if err := c.Uninstall(ctx, "icewhale_photos"); !errors.Is(err, ErrProtected) {
		t.Fatalf("built-in uninstall: %v", err)
	}
	if err := c.Uninstall(ctx, "cron"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("not installed: %v", err)
	}
	if err := c.Uninstall(ctx, "../etc"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("bad name: %v", err)
	}
	if rec.has("zpkg") {
		t.Fatalf("zpkg ran: %v", rec.cmds)
	}
	must(t, os.WriteFile(filepath.Join(rec.ext, "cron.raw"), []byte("x"), 0o644))
	must(t, c.Uninstall(ctx, "cron"))
	if !rec.has("zpkg remove cron") || !rec.has("systemctl daemon-reload") {
		t.Fatalf("uninstall: %v", rec.cmds)
	}
}

func TestDownloadRefusesForeignHostsAndOversize(t *testing.T) {
	c, g, rec := setup(t)
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) }))
	t.Cleanup(foreign.Close)
	err := c.download(context.Background(), foreign.URL+"/x.raw", filepath.Join(t.TempDir(), "x"), sum("x"))
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("download from a host that is not allowed: %v", err)
	}
	big := strings.Repeat("a", 2<<20)
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": big, "cron-amd64.raw.sha256": sum(big)}
	err = c.Install(context.Background(), "gh:lintux/cron")
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversize image: %v", err)
	}
	if rec.has("zpkg") {
		t.Fatalf("zpkg ran: %v", rec.cmds)
	}
}

func TestListFlagsAReleaseWithoutChecksum(t *testing.T) {
	c, g, _ := setup(t)
	g.releases["lintux/cron"] = map[string]string{"cron.raw": "x"}
	entries, _, err := c.List(context.Background())
	must(t, err)
	for _, e := range entries {
		if e.ID == "gh:lintux/cron" && !strings.Contains(e.Error, "no checksum") {
			t.Fatalf("entry without checksum not flagged: %+v", e)
		}
	}
}

func TestProtectedMessageFitsInstallToo(t *testing.T) {
	if strings.Contains(ErrProtected.Error(), "removed") {
		t.Fatalf("protected message speaks of removal only: %q", ErrProtected)
	}
}

func TestRateLimitServesTheLastGoodAnswer(t *testing.T) {
	c, g, _ := setup(t)
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": "x", "cron-amd64.raw.sha256": sum("x")}
	g.tags["lintux/cron"] = "v0.3.3"
	if _, _, err := c.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	g.limited = true
	c.cache = nil
	for k, v := range c.good { // pretend the good answer is older than the TTL
		v.At = v.At.Add(-time.Hour)
		c.good[k] = v
	}
	entries, _, err := c.List(context.Background())
	must(t, err)
	for _, e := range entries {
		if e.ID == "gh:lintux/cron" {
			if e.Version != "v0.3.3" || e.Error != "" || !strings.Contains(e.Note, "rate limit") {
				t.Fatalf("rate-limited entry %+v: want the cached v0.3.3 with a note", e)
			}
		}
		if e.ID == "gh:lintux/zfw" && !strings.Contains(e.Error, "rate limit") {
			t.Errorf("never fetched before, rate limited: want the error, got %+v", e)
		}
	}
}

func TestReleaseCacheSurvivesARestart(t *testing.T) {
	c, g, rec := setup(t)
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": "x", "cron-amd64.raw.sha256": sum("x")}
	g.releases["lintux/zfw"] = map[string]string{"zfw-amd64.tar.gz": "x"}
	g.releases["lintux/tailscale"] = map[string]string{"tailscale-amd64.raw": "t", "tailscale-amd64.raw.sha256": sum("t")}
	if _, _, err := c.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := g.apiCalls
	restarted := &Catalog{Run: rec.run, HTTP: c.HTTP, GitHubAPI: c.GitHubAPI, Gateway: c.Gateway,
		StateDir: c.StateDir, ExtDir: c.ExtDir, Arch: c.Arch, MaxBytes: c.MaxBytes}
	if _, _, err := restarted.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g.apiCalls != first {
		t.Fatalf("restart made %d new API calls within the TTL", g.apiCalls-first)
	}
}

// The cases of lintux-modkit tools/test-semver.sh: the catalog must order
// versions exactly like the installer ("lt" = a < b, "ge" = a >= b).
func TestCompareVersionsMatchesTheInstaller(t *testing.T) {
	cases := []struct {
		a, b string
		lt   bool
	}{
		{"0.2.1", "0.3.0", true}, {"0.3.0", "0.2.1", false}, {"0.3.0", "0.3.0", false},
		{"0.9.0", "0.10.0", true}, {"1.0.0", "0.99.99", false},
		{"0.3.0-dev7", "0.3.0", true}, {"0.3.0", "0.3.0-dev7", false}, {"0.3.0-rc1", "0.3.0", true},
		{"0.3.0-dev7", "0.2.1", false}, {"0.2.1", "0.3.0-dev7", true},
		{"0.3.0-dev7", "0.3.0-dev10", true}, {"0.3.0-dev10", "0.3.0-dev7", false}, {"0.3.0-dev7", "0.3.0-dev7", false},
		{"0.3.0+abc", "0.3.0", false}, {"0.3.0", "0.3.0+abc", false}, {"0.3.0-dev7+abc", "0.3.0", true},
		{"v0.3.2", "0.3.3", true},
		{"0.3.0-dev7+abc", "0.3.0-dev10", true}, {"0.3.0-dev10", "0.3.0-dev7+abc", false},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b) < 0; got != c.lt {
			t.Errorf("CompareVersions(%q, %q) < 0 = %v, want %v", c.a, c.b, got, c.lt)
		}
	}
}

func TestInstalledVersionDecidesTheState(t *testing.T) {
	cases := []struct{ running, tag, state string }{
		{"0.3.2", "v0.3.3", "update"},
		{"0.3.3", "v0.3.3", "current"},
		{"0.3.4-dev2", "v0.3.3", "newer"},
		{"0.3.3-dev9", "v0.3.3", "update"},
	}
	for _, tc := range cases {
		c, g, rec := setup(t)
		g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": "img", "cron-amd64.raw.sha256": sum("img")}
		g.tags["lintux/cron"] = tc.tag
		g.health["/cron/health"] = tc.running
		// the installed file equals the release image: only the version may decide
		must(t, os.WriteFile(filepath.Join(rec.ext, "cron.raw"), []byte("img"), 0o644))
		entries, _, err := c.List(context.Background())
		must(t, err)
		for _, e := range entries {
			if e.ID == "gh:lintux/cron" && (e.State != tc.state || e.InstalledVersion != tc.running) {
				t.Errorf("running %s, release %s: state %q version %q, want %q", tc.running, tc.tag, e.State, e.InstalledVersion, tc.state)
			}
		}
	}
}

func TestVersionFromExtensionRelease(t *testing.T) {
	c, g, rec := setup(t)
	g.releases["lintux/tailscale"] = map[string]string{"tailscale-amd64.raw": "t", "tailscale-amd64.raw.sha256": sum("t")}
	g.tags["lintux/tailscale"] = "v1.0.4"
	must(t, os.WriteFile(filepath.Join(rec.ext, "tailscale.raw"), []byte("other build"), 0o644))
	must(t, os.MkdirAll(filepath.Join(c.UsrLib, "extension-release.d"), 0o755))
	must(t, os.WriteFile(filepath.Join(c.UsrLib, "extension-release.d", "extension-release.tailscale"), []byte("ID=_any\nSYSEXT_VERSION_ID=1.0.4\n"), 0o644))
	entries, _, err := c.List(context.Background())
	must(t, err)
	for _, e := range entries {
		if e.ID == "gh:lintux/tailscale" && (e.InstalledVersion != "1.0.4" || e.State != "current") {
			t.Fatalf("got %+v, want version 1.0.4 current", e)
		}
	}
}

func TestWithoutAVersionTheChecksumStillDecides(t *testing.T) {
	c, g, rec := setup(t)
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": "new", "cron-amd64.raw.sha256": sum("new")}
	g.health["/cron/health"] = "not a version at all"
	must(t, os.WriteFile(filepath.Join(rec.ext, "cron.raw"), []byte("old"), 0o644))
	entries, _, err := c.List(context.Background())
	must(t, err)
	for _, e := range entries {
		if e.ID == "gh:lintux/cron" && (e.InstalledVersion != "" || e.State != "update") {
			t.Fatalf("got %+v: an unusable version must be ignored and the checksum decide", e)
		}
	}
}

func TestOwnInstallerWithoutImageUsesItsHealthRoute(t *testing.T) {
	c, g, _ := setup(t)
	g.releases["lintux/zfw"] = map[string]string{"zfw-1.0.29-amd64.tar.gz": "x"}
	g.tags["lintux/zfw"] = "v1.0.29"
	entries, _, err := c.List(context.Background())
	must(t, err)
	for _, e := range entries {
		if e.ID == "gh:lintux/zfw" && e.Installed {
			t.Fatalf("no health answer, still reported installed: %+v", e)
		}
	}
	g.health["/v2/zfw/api/health"] = "1.0.28"
	c.cache = nil
	entries, _, err = c.List(context.Background())
	must(t, err)
	for _, e := range entries {
		if e.ID == "gh:lintux/zfw" && (!e.Installed || e.InstalledVersion != "1.0.28" || e.State != "update") {
			t.Fatalf("got %+v, want installed 1.0.28 with update", e)
		}
	}
}

func TestAnyModuleReportsSysextVersionID(t *testing.T) {
	c, g, rec := setup(t)
	must(t, os.MkdirAll(c.StateDir, 0o755))
	must(t, os.WriteFile(filepath.Join(c.StateDir, "sources.json"), []byte(`{"repos":["me/tool"]}`), 0o644))
	g.releases["me/tool"] = map[string]string{"tool-amd64.raw": "new", "tool-amd64.raw.sha256": sum("new")}
	g.tags["me/tool"] = "v2.0.0"
	must(t, os.WriteFile(filepath.Join(rec.ext, "tool.raw"), []byte("old"), 0o644))
	must(t, os.MkdirAll(filepath.Join(c.UsrLib, "extension-release.d"), 0o755))
	must(t, os.WriteFile(filepath.Join(c.UsrLib, "extension-release.d", "extension-release.tool"), []byte("ID=_any\nARCHITECTURE=x86-64\nSYSEXT_VERSION_ID=1.9.0\n"), 0o644))
	entries, _, err := c.List(context.Background())
	must(t, err)
	for _, e := range entries {
		if e.ID == "gh:me/tool" && (e.InstalledVersion != "1.9.0" || e.State != "update") {
			t.Fatalf("got %+v, want 1.9.0 with update to v2.0.0", e)
		}
	}
}

func TestHealthWinsOverExtensionRelease(t *testing.T) {
	c, g, rec := setup(t)
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": "x", "cron-amd64.raw.sha256": sum("x")}
	g.tags["lintux/cron"] = "v0.3.3"
	g.health["/cron/health"] = "0.3.3"
	must(t, os.WriteFile(filepath.Join(rec.ext, "cron.raw"), []byte("x"), 0o644))
	must(t, os.MkdirAll(filepath.Join(c.UsrLib, "extension-release.d"), 0o755))
	must(t, os.WriteFile(filepath.Join(c.UsrLib, "extension-release.d", "extension-release.cron"), []byte("SYSEXT_VERSION_ID=0.1.0\n"), 0o644))
	entries, _, err := c.List(context.Background())
	must(t, err)
	for _, e := range entries {
		if e.ID == "gh:lintux/cron" && e.InstalledVersion != "0.3.3" {
			t.Fatalf("got %q: the running daemon is the better witness than the image", e.InstalledVersion)
		}
	}
}
