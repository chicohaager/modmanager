package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chicohaager/modmanager/internal/catalog"
	"github.com/chicohaager/modmanager/internal/state"
	"github.com/chicohaager/modmanager/internal/sysapps"
	"github.com/chicohaager/modmanager/internal/updates"
)

type fakeSvc struct {
	calls  []string
	err    error
	ctxErr error
}

func (f *fakeSvc) List(context.Context) ([]sysapps.App, error) {
	return []sysapps.App{{ID: "module:icewhale_photos", Kind: sysapps.KindBase, Services: []sysapps.Service{}}}, nil
}
func (f *fakeSvc) Hide(ctx context.Context, id string) error {
	f.calls = append(f.calls, "hide "+id)
	f.ctxErr = ctx.Err()
	return f.err
}
func (f *fakeSvc) Restore(_ context.Context, id string) error {
	f.calls = append(f.calls, "restore "+id)
	return f.err
}
func (f *fakeSvc) Purge(_ context.Context, id, confirm string) error {
	f.calls = append(f.calls, "purge "+id+" "+confirm)
	return f.err
}

// requireToken stands in for auth.Verifier.Middleware.
func requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func do(t *testing.T, h http.Handler, method, path, body string, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	req.Header.Set("Authorization", "Bearer x")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestAppsNeedAToken(t *testing.T) {
	mux := newMux(&fakeSvc{}, &fakeCat{}, requireToken)
	for _, p := range []string{"/modmanager/apps", "/modmanager/apps/hide", "/modmanager/apps/restore", "/modmanager/apps/purge"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s without token: %d", p, w.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/modmanager/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("health: %d", w.Code)
	}
}

func TestActionsCallTheServiceAndReturnTheList(t *testing.T) {
	svc := &fakeSvc{}
	mux := newMux(svc, &fakeCat{}, requireToken)
	w := do(t, mux, http.MethodPost, "/modmanager/apps/hide", `{"id":"module:icewhale_photos"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("hide: %d %s", w.Code, w.Body)
	}
	var resp struct{ Apps []sysapps.App }
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || len(resp.Apps) != 1 {
		t.Fatalf("response %s: %v", w.Body, err)
	}
	do(t, mux, http.MethodPost, "/modmanager/apps/purge", `{"id":"module:icewhale_photos","confirm":"icewhale_photos"}`, nil)
	want := []string{"hide module:icewhale_photos", "purge module:icewhale_photos icewhale_photos"}
	if strings.Join(svc.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %v, want %v", svc.calls, want)
	}
	if w := do(t, mux, http.MethodGet, "/modmanager/apps/hide", "", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET hide: %d", w.Code)
	}
	if w := do(t, mux, http.MethodPost, "/modmanager/apps/hide", `{}`, nil); w.Code != http.StatusBadRequest {
		t.Errorf("hide without id: %d", w.Code)
	}
}

func TestActionSurvivesAClosedTab(t *testing.T) {
	svc := &fakeSvc{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	do(t, newMux(svc, &fakeCat{}, requireToken), http.MethodPost, "/modmanager/apps/hide", `{"id":"x"}`, ctx)
	if svc.ctxErr != nil {
		t.Fatalf("the action ran with a cancelled context: %v", svc.ctxErr)
	}
}

func TestServiceErrorsMapToStatusAndCode(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{sysapps.ErrUnknown, 404, "unknown_app"},
		{sysapps.ErrBlocked, 409, "blocked"},
		{sysapps.ErrNotHid, 409, "not_hidden"},
		{sysapps.ErrMedia, 409, "media_found"},
		{sysapps.ErrConfirm, 400, "confirm_mismatch"},
		{context.DeadlineExceeded, 500, "internal"},
	}
	for _, c := range cases {
		w := do(t, newMux(&fakeSvc{err: c.err}, &fakeCat{}, requireToken), http.MethodPost, "/modmanager/apps/restore", `{"id":"x"}`, nil)
		if w.Code != c.status || !strings.Contains(w.Body.String(), `"`+c.code+`"`) {
			t.Errorf("%v: %d %s, want %d %s", c.err, w.Code, w.Body, c.status, c.code)
		}
	}
}

type fakeCat struct {
	calls []string
	err   error
}

func (f *fakeCat) List(_ context.Context) ([]catalog.Entry, []string, error) {
	return []catalog.Entry{{ID: "gh:o/r"}}, []string{"o/r"}, nil
}
func (f *fakeCat) Install(_ context.Context, id string) error {
	f.calls = append(f.calls, "install "+id)
	return f.err
}
func (f *fakeCat) Uninstall(_ context.Context, name string) error {
	f.calls = append(f.calls, "uninstall "+name)
	return f.err
}
func (f *fakeCat) AddRepo(_ context.Context, repo string) (string, error) {
	f.calls = append(f.calls, "add "+repo)
	return repo, f.err
}
func (f *fakeCat) RemoveRepo(repo string) error {
	f.calls = append(f.calls, "remove "+repo)
	return f.err
}

func (f *fakeCat) Self(_ context.Context, installed string) catalog.SelfStatus {
	return catalog.SelfStatus{Installed: installed, Latest: "v9.9.9", Update: true}
}
func (f *fakeCat) SelfUpdate(_ context.Context, installed string) (string, error) {
	f.calls = append(f.calls, "self-update "+installed)
	return "v9.9.9", f.err
}

func TestSelfRoutes(t *testing.T) {
	cat := &fakeCat{}
	mux := newMux(&fakeSvc{}, cat, requireToken)
	w := do(t, mux, http.MethodGet, "/modmanager/self", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"installed":"`+version+`"`) || !strings.Contains(w.Body.String(), `"update":true`) {
		t.Fatalf("self: %d %s", w.Code, w.Body)
	}
	w = do(t, mux, http.MethodPost, "/modmanager/self/update", `{}`, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"v9.9.9"`) || strings.Join(cat.calls, "|") != "self-update "+version {
		t.Fatalf("update: %d %s calls %v", w.Code, w.Body, cat.calls)
	}
	if w := do(t, newMux(&fakeSvc{}, &fakeCat{err: catalog.ErrNotNewer}, requireToken), http.MethodPost, "/modmanager/self/update", `{}`, nil); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"not_newer"`) {
		t.Fatalf("not newer: %d %s", w.Code, w.Body)
	}
	for _, p := range []string{"/modmanager/self", "/modmanager/self/update"} {
		rw := httptest.NewRecorder()
		mux.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, p, nil))
		if rw.Code != http.StatusUnauthorized {
			t.Errorf("%s without token: %d", p, rw.Code)
		}
	}
}

func TestCatalogRoutes(t *testing.T) {
	cat := &fakeCat{}
	mux := newMux(&fakeSvc{}, cat, requireToken)
	w := do(t, mux, http.MethodGet, "/modmanager/catalog", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	do(t, mux, http.MethodPost, "/modmanager/catalog/install", `{"id":"gh:o/r"}`, nil)
	do(t, mux, http.MethodPost, "/modmanager/catalog/uninstall", `{"name":"cron"}`, nil)
	do(t, mux, http.MethodPost, "/modmanager/catalog/repos", `{"repo":"o/r"}`, nil)
	do(t, mux, http.MethodPost, "/modmanager/catalog/repos/remove", `{"repo":"o/r"}`, nil)
	want := "install gh:o/r|uninstall cron|add o/r|remove o/r"
	if strings.Join(cat.calls, "|") != want {
		t.Fatalf("calls %v, want %s", cat.calls, want)
	}
	for _, p := range []string{"/modmanager/catalog", "/modmanager/catalog/install"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rw := httptest.NewRecorder()
		mux.ServeHTTP(rw, req)
		if rw.Code != http.StatusUnauthorized {
			t.Errorf("%s without token: %d", p, rw.Code)
		}
	}
}

func TestCatalogErrorsMapToStatusAndCode(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{catalog.ErrBadRepo, 400, "bad_repo"},
		{catalog.ErrUnknown, 404, "unknown_entry"},
		{catalog.ErrNoImage, 422, "no_image"},
		{catalog.ErrNoChecksum, 422, "no_checksum"},
		{catalog.ErrChecksum, 422, "checksum_mismatch"},
		{catalog.ErrOwn, 409, "own_installer"},
		{catalog.ErrProtected, 409, "protected"},
		{catalog.ErrNotInstalled, 409, "not_installed"},
		{fmt.Errorf("o/r: %w", catalog.ErrNoRelease), 404, "no_release"},
		{fmt.Errorf("%w: dial tcp", catalog.ErrUnreachable), 502, "unreachable"},
		{catalog.ErrRateLimit, 429, "rate_limit"},
		{context.DeadlineExceeded, 502, "failed"},
	}
	for _, c := range cases {
		w := do(t, newMux(&fakeSvc{}, &fakeCat{err: c.err}, requireToken), http.MethodPost, "/modmanager/catalog/install", `{"id":"x"}`, nil)
		if w.Code != c.status || !strings.Contains(w.Body.String(), `"`+c.code+`"`) {
			t.Errorf("%v: %d %s, want %d %s", c.err, w.Code, w.Body, c.status, c.code)
		}
	}
}

// The page reports its language, so the dashboard card about module updates speaks it.
func TestUILangIsStoredForTheDashboardCard(t *testing.T) {
	langDir = t.TempDir()
	mux := newMux(&fakeSvc{}, &fakeCat{}, requireToken)
	if w := do(t, mux, http.MethodPost, "/modmanager/ui-lang", `{"lang":"de"}`, nil); w.Code != http.StatusOK {
		t.Fatalf("de: %d %s", w.Code, w.Body)
	}
	if got := updates.ReadLang(langDir); got != "de" {
		t.Fatalf("stored %q", got)
	}
	if w := do(t, mux, http.MethodPost, "/modmanager/ui-lang", `{"lang":"xx"}`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("xx: %d", w.Code)
	}
	if w := do(t, mux, http.MethodPost, "/modmanager/ui-lang", `{"lang":"fr"}`, nil); w.Code != http.StatusOK || updates.ReadLang(langDir) != "fr" {
		t.Fatalf("fr: %d, stored %q", w.Code, updates.ReadLang(langDir))
	}
	if w := do(t, mux, http.MethodPost, "/modmanager/ui-lang", `{"lang":"de"}`, nil); w.Code != http.StatusOK {
		t.Fatalf("de again: %d", w.Code)
	}
	if w := do(t, mux, http.MethodGet, "/modmanager/ui-lang", ``, nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", w.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/modmanager/ui-lang", strings.NewReader(`{"lang":"en"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || updates.ReadLang(langDir) != "de" {
		t.Fatalf("without a token: %d, stored %q", w.Code, updates.ReadLang(langDir))
	}
}

// After an install the dashboard card about module updates is checked again at once.
func TestACatalogChangeAsksForAnUpdateCheck(t *testing.T) {
	n := 0
	onCatalogChange = func() { n++ }
	t.Cleanup(func() { onCatalogChange = func() {} })
	mux := newMux(&fakeSvc{}, &fakeCat{}, requireToken)
	if w := do(t, mux, http.MethodPost, "/modmanager/catalog/install", `{"id":"gh:a/b"}`, nil); w.Code != http.StatusOK {
		t.Fatalf("install: %d %s", w.Code, w.Body)
	}
	if n != 1 {
		t.Fatalf("update checks asked for: %d, want 1", n)
	}
	_ = do(t, newMux(&fakeSvc{}, &fakeCat{err: catalog.ErrNotNewer}, requireToken), http.MethodPost, "/modmanager/catalog/install", `{"id":"gh:a/b"}`, nil)
	if n != 1 {
		t.Fatalf("a failed install asked for a check: %d", n)
	}
}

// Every part keeps its state in the root-only directory, none in /DATA/AppData
// (0777 without the sticky bit on ZimaOS 1.8, internal/state).
func TestEveryPartUsesTheRootOnlyStateDir(t *testing.T) {
	if got := catalog.New(nil).StateDir; got != state.Dir {
		t.Errorf("catalog: %s", got)
	}
	if got := sysapps.DefaultPaths().StateDir; got != state.Dir {
		t.Errorf("sysapps: %s", got)
	}
	if defaultLangDir != state.Dir {
		t.Errorf("ui-lang: %s", defaultLangDir)
	}
	if state.Dir != "/DATA/.modmanager" || state.OldDir != "/DATA/AppData/modmanager" {
		t.Errorf("dirs %s %s", state.Dir, state.OldDir)
	}
}
