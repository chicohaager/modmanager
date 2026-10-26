package updates

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chicohaager/modmanager/internal/catalog"
)

type fakeSource struct {
	entries []catalog.Entry
	err     error
	self    catalog.SelfStatus
}

func (f fakeSource) List(context.Context) ([]catalog.Entry, []string, error) {
	return f.entries, nil, f.err
}
func (f fakeSource) Self(context.Context, string) catalog.SelfStatus { return f.self }

func TestFindTakesInstalledModulesWithANewerReleaseAndModuleManagerItself(t *testing.T) {
	src := fakeSource{
		entries: []catalog.Entry{
			{Title: "Nebula", Installed: true, State: "update", InstalledVersion: "1.1.0", Version: "v1.1.1"},
			{Title: "mergerfs + SnapRAID", Installed: true, State: "update", Version: "v1.2.1"}, // compared by checksum, no version
			{Title: "WireGuard", Installed: true, State: "current", Version: "v1.1.0"},
			{Title: "restic Backup", Installed: false, Version: "v1.0.0"},
			{Title: "ZFW Firewall", Installed: true, State: "newer", Version: "v1.0.30"},
			{Title: "Broken", Error: "rate limit", ErrorCode: "rate_limit"},
		},
		self: catalog.SelfStatus{Installed: "0.3.0", Latest: "v0.3.1", Update: true},
	}
	got, err := Find(context.Background(), src, "0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	want := []Update{
		{Title: "Module Manager", From: "0.3.0", To: "v0.3.1"},
		{Title: "Nebula", From: "1.1.0", To: "v1.1.1"},
		{Title: "mergerfs + SnapRAID", To: "v1.2.1"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
}

func TestFindReportsAFailedList(t *testing.T) {
	if _, err := Find(context.Background(), fakeSource{err: errors.New("no state dir")}, "0.3.0"); err == nil {
		t.Fatal("a failed catalog list must not look like 'no updates'")
	}
}

func TestCardTextInEveryLanguageAndWithinTheVisibleBudget(t *testing.T) {
	one := []Update{{Title: "Nebula", From: "1.1.0", To: "v1.1.1"}}
	if got := Text("de", one); got != "1 Modul-Update: Nebula 1.1.0 → v1.1.1" {
		t.Errorf("de: %q", got)
	}
	if got := Text("en", one); got != "1 module update: Nebula 1.1.0 → v1.1.1" {
		t.Errorf("en: %q", got)
	}
	if got := Text("fr", one); got != "1 mise à jour de module : Nebula 1.1.0 → v1.1.1" {
		t.Errorf("fr: %q", got)
	}
	if got := Text("xx", one); !strings.HasPrefix(got, "1 module update") {
		t.Errorf("unknown language falls back to English: %q", got)
	}
	many := make([]Update, 9)
	for i := range many {
		many[i] = Update{Title: "Module number " + string(rune('A'+i)), From: "1.0.0", To: "v1.0.1"}
	}
	for lang, lead := range map[string]string{"de": "9 Modul-Updates: ", "fr": "9 mises à jour de modules : "} {
		got := Text(lang, many)
		if !strings.HasPrefix(got, lead) || len([]rune(got)) > budget || !strings.HasSuffix(got, "…") {
			t.Errorf("%s long list: %d runes %q", lang, len([]rune(got)), got)
		}
	}
}

// bus is a fake ZimaOS message bus that records the events it receives.
type bus struct {
	mu     sync.Mutex
	events []string
	bodies []map[string]string
}

func newBus(t *testing.T) (*bus, string) {
	b := &bus{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]string
		_ = json.Unmarshal(raw, &m)
		b.mu.Lock()
		b.events = append(b.events, strings.TrimPrefix(r.URL.Path, "/v2/message_bus/event/ysk/"))
		b.bodies = append(b.bodies, m)
		b.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	f := filepath.Join(t.TempDir(), "message-bus.url")
	_ = os.WriteFile(f, []byte(srv.URL+"\n"), 0o644)
	return b, f
}

func TestKeeperShowsWhileUpdatesExistAndWithdrawsOnce(t *testing.T) {
	b, f := newBus(t)
	list := []Update{{Title: "Nebula", From: "1.1.0", To: "v1.1.1"}}
	k := &Keeper{Notifier: Notifier{BusURLFile: f}, Lang: func() string { return "de" }}
	k.Set(list)
	k.Round(context.Background())
	k.Round(context.Background())
	k.Set(nil)
	k.Round(context.Background())
	k.Round(context.Background())
	if got := strings.Join(b.events, " "); got != "ysk:card:upsert ysk:card:upsert ysk:card:delete" {
		t.Fatalf("events %q", got)
	}
	var c card
	if err := json.Unmarshal([]byte(b.bodies[0]["card:body"]), &c); err != nil {
		t.Fatal(err)
	}
	if c.ID != CardID || c.Content.BodyIconWithText.Description != "1 Modul-Update: Nebula 1.1.0 → v1.1.1" ||
		c.Content.FooterActions[0].Text != "Module Manager öffnen" ||
		c.Content.FooterActions[0].MessageBus.Payload != `{"url":"modules/modmanager/index.html#catalog"}` {
		t.Fatalf("card %+v", c)
	}
	if b.bodies[2]["card:id"] != CardID {
		t.Fatalf("delete body %v", b.bodies[2])
	}
}

func TestKeeperWithdrawsACardOfThePreviousRunAtStart(t *testing.T) {
	b, f := newBus(t)
	k := &Keeper{Notifier: Notifier{BusURLFile: f}, Lang: func() string { return "en" }}
	k.Round(context.Background()) // no check has run yet: nothing known, nothing sent
	k.Set(nil)                    // the first check found nothing
	k.Round(context.Background())
	if got := strings.Join(b.events, " "); got != "ysk:card:delete" {
		t.Fatalf("events %q", got)
	}
}

func TestTheBusIsOnlyReachedOnLoopback(t *testing.T) {
	f := filepath.Join(t.TempDir(), "message-bus.url")
	_ = os.WriteFile(f, []byte("http://192.0.2.7:37065\n"), 0o644)
	if err := (Notifier{BusURLFile: f}).Withdraw(context.Background()); err == nil {
		t.Fatal("a bus URL outside 127.0.0.1 must be refused")
	}
}

func TestLangFile(t *testing.T) {
	dir := t.TempDir()
	if got := ReadLang(dir); got != "en" {
		t.Errorf("no file: %q", got)
	}
	if err := WriteLang(dir, "de"); err != nil {
		t.Fatal(err)
	}
	if got := ReadLang(dir); got != "de" {
		t.Errorf("after de: %q", got)
	}
	if err := WriteLang(dir, "fr"); err != nil {
		t.Fatal(err)
	}
	if got := ReadLang(dir); got != "fr" {
		t.Errorf("after fr: %q", got)
	}
	for _, bad := range []string{"fr\n../x", "xx", ""} {
		if err := WriteLang(dir, bad); err == nil {
			t.Errorf("%q stored: only en, de and fr are", bad)
		}
	}
	if got := ReadLang(dir); got != "fr" {
		t.Errorf("a refused write changed the stored language: %q", got)
	}
}

// countingSource answers with the list it holds and counts the checks.
type countingSource struct {
	mu    sync.Mutex
	n     int
	state string
}

func (c *countingSource) List(context.Context) ([]catalog.Entry, []string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return []catalog.Entry{{Title: "Nebula", Installed: true, State: c.state, InstalledVersion: "1.0.9", Version: "v1.1.0"}}, nil, nil
}
func (c *countingSource) Self(context.Context, string) catalog.SelfStatus {
	return catalog.SelfStatus{}
}
func (c *countingSource) checks() int { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

// An install from the catalog must not leave the card standing until the next 12-hour check.
func TestKickChecksAgainAtOnce(t *testing.T) {
	b, f := newBus(t)
	src := &countingSource{state: "update"}
	k := &Keeper{Notifier: Notifier{BusURLFile: f}, Lang: func() string { return "en" }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx, k, src, "0.3.0", 0, time.Hour, time.Hour)
	waitFor(t, func() bool { return src.checks() == 1 })
	src.mu.Lock()
	src.state = "current" // the user installed the update
	src.mu.Unlock()
	k.Kick()
	waitFor(t, func() bool { return src.checks() == 2 })
	waitFor(t, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.events) > 0 && b.events[len(b.events)-1] == "ysk:card:delete"
	})
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached in 2 s")
}

// An update installed past the catalog (a module's own installer, zpkg by hand) gets no Kick:
// while the card names updates, the check runs at the short interval, not after 12 hours.
func TestWhileUpdatesWaitTheCheckRunsSooner(t *testing.T) {
	_, f := newBus(t)
	src := &countingSource{state: "update"}
	k := &Keeper{Notifier: Notifier{BusURLFile: f}, Lang: func() string { return "en" }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx, k, src, "0.3.0", 0, time.Hour, 20*time.Millisecond)
	waitFor(t, func() bool { return src.checks() >= 3 })
	src.mu.Lock()
	src.state = "current"
	src.mu.Unlock()
	waitFor(t, func() bool { return src.checks() >= 4 })
	n := src.checks()
	time.Sleep(100 * time.Millisecond)
	if src.checks() != n {
		t.Fatalf("with nothing waiting the check kept running at the short interval: %d → %d", n, src.checks())
	}
}
