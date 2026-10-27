package loops

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chicohaager/lintux-modkit/ysk"
)

// sysTree writes /sys/block/loopN/loop/backing_file like the kernel does (measured on
// .184: "/zfw.raw", "/web-ftp-client.raw (deleted)", "/.zpkg-backups/restic.raw.bak (deleted)").
func sysTree(t *testing.T, files ...string) string {
	t.Helper()
	sys := t.TempDir()
	for i, f := range files {
		d := filepath.Join(sys, fmt.Sprintf("loop%d", i))
		if f == "" { // an unbound loop device has no backing_file
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Join(d, "loop"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "loop", "backing_file"), []byte(f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(sys, "sda"), 0o755); err != nil { // not a loop device
		t.Fatal(err)
	}
	return sys
}

func TestMeasureCountsOldStatesPerImage(t *testing.T) {
	sys := sysTree(t,
		"/zfw.raw", "/zfw.raw", "/zfw.raw",
		"/web-ftp-client.raw (deleted)", "/web-ftp-client.raw", "/web-ftp-client.raw",
		"/.zpkg-backups/restic.raw.bak (deleted)", "/restic.raw",
		"/lib/zimaos/modules/icewhale_files.raw",
		"",                  // unbound
		"/DATA/vm/disk.img", // a loop device that is no extension image
	)
	st, err := Measure(sys)
	if err != nil {
		t.Fatal(err)
	}
	if st.Loops != 9 || st.Images != 4 || st.OldStates != 2 || st.Threshold != Threshold {
		t.Fatalf("%+v, want 9 loops, 4 images, 2 old states", st)
	}
}

func TestNoLoopDevicesNoOldStates(t *testing.T) {
	st, err := Measure(t.TempDir())
	if err != nil || st.Loops != 0 || st.OldStates != 0 {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestTextNamesBothNumbersInEveryLanguage(t *testing.T) {
	st := Stats{Loops: 634, OldStates: 30}
	for _, lang := range []string{"en", "de", "fr", "xx"} {
		s := Text(lang, st)
		if !strings.Contains(s, "30") || !strings.Contains(s, "634") || strings.Contains(s, "{") {
			t.Errorf("%s: %q", lang, s)
		}
	}
	if Text("xx", st) != Text("en", st) {
		t.Error("an unknown language must fall back to English")
	}
}

type bus struct {
	mu     sync.Mutex
	events []string
	bodies []string
}

func newBus(t *testing.T) (*bus, string) {
	b := &bus{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.events = append(b.events, strings.TrimPrefix(r.URL.Path, "/v2/message_bus/event/ysk/"))
		b.bodies = append(b.bodies, string(raw))
		b.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	f := filepath.Join(t.TempDir(), "message-bus.url")
	if err := os.WriteFile(f, []byte(srv.URL+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return b, f
}

func states(n int) []string {
	var f []string
	for i := 0; i <= n; i++ {
		f = append(f, "/zfw.raw", "/cron.raw")
	}
	return f
}

// The card shows from the threshold on, is re-sent every round (ZimaOS stores no card)
// and withdrawn once when the number falls (after a reboot).
func TestTheCardFollowsTheThreshold(t *testing.T) {
	b, f := newBus(t)
	k := &Keeper{Notifier: ysk.Notifier{BusURLFile: f}, Lang: func() string { return "de" }}
	k.Sys = sysTree(t, states(Threshold-1)...)
	k.Round(context.Background())
	if len(b.events) != 0 {
		t.Fatalf("below the threshold: %v", b.events)
	}
	k.Sys = sysTree(t, states(Threshold)...)
	k.Round(context.Background())
	k.Round(context.Background())
	k.Sys = sysTree(t, states(2)...)
	k.Round(context.Background())
	k.Round(context.Background())
	if got := strings.Join(b.events, " "); got != "ysk:card:upsert ysk:card:upsert ysk:card:delete" {
		t.Fatalf("events %q", got)
	}
	if !strings.Contains(b.bodies[0], CardID) || !strings.Contains(b.bodies[0], "alte Systemstände") || !strings.Contains(b.bodies[0], "22 Loop-Geräte") {
		t.Errorf("card %s", b.bodies[0])
	}
	if got := k.Last(); got.OldStates != 2 || got.Loops != 6 {
		t.Errorf("last %+v", got)
	}
}

// After a restart of Module Manager a card of the previous run may still be on an open
// dashboard: the first round below the threshold withdraws it.
func TestRunWithdrawsACardOfThePreviousRun(t *testing.T) {
	b, f := newBus(t)
	k := &Keeper{Notifier: ysk.Notifier{BusURLFile: f}, Lang: func() string { return "en" }, Sys: sysTree(t, states(1)...)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Run(ctx, k); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		n := len(b.events)
		b.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	b.mu.Lock()
	defer b.mu.Unlock()
	if got := strings.Join(b.events, " "); got != "ysk:card:delete" {
		t.Fatalf("events %q", got)
	}
}

// procTree writes /proc/<pid>/{mountinfo,cgroup,ns/mnt} for each pid.
func procTree(t *testing.T, procs map[string][3]string) string {
	t.Helper()
	proc := t.TempDir()
	for pid, p := range procs {
		d := filepath.Join(proc, pid)
		if err := os.MkdirAll(filepath.Join(d, "ns"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "mountinfo"), []byte(p[0]), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "cgroup"), []byte(p[1]), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(p[2], filepath.Join(d, "ns", "mnt")); err != nil {
			t.Fatal(err)
		}
	}
	return proc
}

// The stacking as measured on .184 (systemd-logind, 2026-10-27): the old /usr of the
// service's start sits on the current one and comes first in mountinfo.
func TestStaleUsrFindsServicesWithTheOldUsrOnTop(t *testing.T) {
	host := "24 1 0:317 / /usr ro,nodev,relatime shared:9 - overlay sysext ro,lowerdir=x\n"
	logind := "391 1558 0:44 / /usr ro,nosuid,nodev,relatime master:9 - overlay sysext ro,lowerdir=x\n" +
		"1558 354 0:317 / /usr ro,nodev,relatime master:9 - overlay sysext ro,lowerdir=x\n"
	current := "1731 1122 0:317 / /usr ro,nodev,relatime master:9 - overlay sysext ro,lowerdir=x\n"
	proc := procTree(t, map[string][3]string{
		"1":   {host, "0::/init.scope\n", "mnt:[1]"},
		"983": {logind, "0::/system.slice/systemd-logind.service\n", "mnt:[2]"},
		"984": {logind, "0::/system.slice/systemd-logind.service\n", "mnt:[2]"}, // same namespace: once
		"990": {logind, "0::/system.slice/virtlogd.service\n", "mnt:[3]"},
		"995": {current, "0::/system.slice/cron.service\n", "mnt:[4]"},
		"999": {"", "0::/system.slice/docker-x.scope\n", "mnt:[5]"}, // a container without /usr from the host
	})
	got := StaleUsr(proc)
	if strings.Join(got, ",") != "systemd-logind.service,virtlogd.service" {
		t.Fatalf("stale %v", got)
	}
}

func TestStaleUsrWithoutAHostUsrIsEmpty(t *testing.T) {
	if got := StaleUsr(t.TempDir()); got == nil || len(got) != 0 {
		t.Fatalf("%#v", got)
	}
}
