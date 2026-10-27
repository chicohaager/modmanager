package sysapps

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chicohaager/modmanager/internal/progress"
)

func logged(l *progress.Log) string {
	var out []string
	for _, ln := range l.Snapshot().Lines {
		k := ln.Key
		if k == "" {
			k = ln.Text
		}
		out = append(out, ln.Kind+" "+k)
	}
	return strings.Join(out, "|")
}

func withJob(action, id string) (*progress.Log, context.Context) {
	l := &progress.Log{}
	l.Start(action, id)
	return l, progress.NewContext(context.Background(), l)
}

// Hiding a base app shows each step and command; reading the state (systemctl show) does not.
func TestHideBaseReportsItsSteps(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	l, ctx := withJob("hide", "module:icewhale_photos")
	must(t, h.m.Hide(ctx, "module:icewhale_photos"))
	got := logged(l)
	want := "step overlay|cmd systemctl disable --now zimaos-photos.service|cmd systemctl mask zimaos-photos.service|" +
		"cmd systemctl disable --now zimaos-photos-ai-worker.service|cmd systemctl mask zimaos-photos-ai-worker.service|" +
		"step refresh|cmd systemd-sysext refresh|cmd systemctl daemon-reload"
	if got != want {
		t.Fatalf("progress\n got %s\nwant %s", got, want)
	}
	l2, ctx2 := withJob("restore", "module:icewhale_photos")
	must(t, h.m.Restore(ctx2, "module:icewhale_photos"))
	if g := logged(l2); !strings.HasPrefix(g, "step removeOverlay|step refresh|cmd systemd-sysext refresh|") || !strings.HasSuffix(g, "cmd systemctl enable --now zimaos-photos-ai-worker.service") {
		t.Fatalf("restore progress %s", g)
	}
}

func TestHideSysextReportsItsSteps(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	l, ctx := withJob("hide", "module:icewhale_peerdrop")
	must(t, h.m.Hide(ctx, "module:icewhale_peerdrop"))
	// peerdrop has no service in this world: nothing to stop
	if g := logged(l); g != "step keepManifest|step mask|step refresh|cmd systemd-sysext refresh|cmd systemctl daemon-reload" {
		t.Fatalf("progress %s", g)
	}
	l2, ctx2 := withJob("restore", "module:icewhale_peerdrop")
	must(t, h.m.Restore(ctx2, "module:icewhale_peerdrop"))
	if g := logged(l2); g != "step unmask|step refresh|cmd systemd-sysext refresh|cmd systemctl daemon-reload" {
		t.Fatalf("restore progress %s", g)
	}
}

func TestSystemTileReportsTheBackupAndTheChange(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	l, ctx := withJob("hide", "system:backup")
	must(t, h.m.Hide(ctx, "system:backup"))
	s := l.Snapshot()
	if g := logged(l); g != "ok userdbBackup|step tileHide" {
		t.Fatalf("progress %s", g)
	}
	if p := s.Lines[0].Args["path"]; !strings.HasPrefix(p, filepath.Join(h.m.P.StateDir, "userdb-backups")+"/") {
		t.Errorf("backup path %q", p)
	}
	l2, ctx2 := withJob("restore", "system:backup")
	must(t, h.m.Restore(ctx2, "system:backup"))
	if g := logged(l2); g != "ok userdbBackup|step tileShow" {
		t.Fatalf("restore progress %s", g)
	}
}

func TestPurgeReportsTheMediaCheckAndTheDeletion(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	data := h.path("photosdata")
	dataDirs["icewhale_photos"] = data
	t.Cleanup(func() { dataDirs["icewhale_photos"] = "/DATA/AppData/photos" })
	must(t, os.MkdirAll(filepath.Join(data, "manticore"), 0o755))
	must(t, h.m.Hide(context.Background(), "module:icewhale_photos"))
	l, ctx := withJob("purge", "module:icewhale_photos")
	must(t, h.m.Purge(ctx, "module:icewhale_photos", "icewhale_photos"))
	if g := logged(l); g != "step mediaCheck|ok noMedia|step delete" {
		t.Fatalf("progress %s", g)
	}
}

// Reconcile at start runs the same refresh without a job in its context: it must not write
// into the job of an action running at that moment.
func TestReconcileDoesNotWriteIntoAnotherJob(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	must(t, h.m.Hide(context.Background(), "module:icewhale_photos"))
	writeJSON(t, filepath.Join(h.base, "icewhale_photos.json"), tile("icewhale_photos", "Photos 2", true, "zimaos-photos.service"))
	l := &progress.Log{}
	l.Start("install", "gh:o/r") // a catalog action at the same time, its log not in Reconcile's context
	changed, err := h.m.Reconcile(context.Background())
	must(t, err)
	if len(changed) != 1 || !h.ran("systemd-sysext refresh") {
		t.Fatalf("reconcile did not run: %v %v", changed, h.cmds)
	}
	if g := logged(l); g != "" {
		t.Fatalf("reconcile wrote into the install's log: %s", g)
	}
}

// With Stream the output of systemd-sysext refresh is in the log while it runs.
func TestRefreshStreamsItsOutput(t *testing.T) {
	h := newHost(t)
	h.world()
	h.userDB()
	var during string
	h.m.Stream = func(ctx context.Context, out io.Writer, name string, args ...string) error {
		if name == "systemd-sysext" {
			fmt.Fprint(out, "Using extensions 'zfw'.\n")
			during = logged(progress.FromContext(ctx))
		}
		_, err := h.run(ctx, name, args...)
		return err
	}
	l, ctx := withJob("hide", "module:icewhale_peerdrop")
	must(t, h.m.Hide(ctx, "module:icewhale_peerdrop"))
	if !strings.HasSuffix(during, "cmd systemd-sysext refresh|out Using extensions 'zfw'.") {
		t.Fatalf("while refresh ran: %s", during)
	}
	if !strings.Contains(logged(l), "|out Using extensions 'zfw'.|cmd systemctl daemon-reload") {
		t.Fatalf("progress %s", logged(l))
	}
}
