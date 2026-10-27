package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chicohaager/modmanager/internal/progress"
)

func lines(l *progress.Log) []string {
	var out []string
	for _, ln := range l.Snapshot().Lines {
		key := ln.Key
		if key == "" {
			key = ln.Text
		}
		out = append(out, ln.Kind+" "+key)
	}
	return out
}

// An update shows every step in the order it runs, with zpkg's output.
func TestInstallReportsEveryStep(t *testing.T) {
	c, g, rec := setup(t)
	c.Progress = &progress.Log{}
	img := "squashfs-cron-v2"
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": img, "cron-amd64.raw.sha256": sum(img)}
	g.tags["lintux/cron"] = "v0.3.3"
	must(t, os.WriteFile(filepath.Join(rec.ext, "cron.raw"), []byte("squashfs-cron-v1"), 0o644))
	c.Progress.Start("install", "gh:lintux/cron")
	must(t, c.Install(context.Background(), "gh:lintux/cron"))
	dl := filepath.Join(c.StateDir, "downloads", "cron.raw")
	want := []string{
		"step release", "step image", "step download", "bar download", "ok checksum",
		"step collisions", "ok noCollision", "step keep",
		"cmd zpkg install --force " + dl, "cmd systemctl daemon-reload", "step services",
	}
	if got := lines(c.Progress); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("progress\n got %v\nwant %v", got, want)
	}
	s := c.Progress.Snapshot()
	if s.Lines[1].Args["tag"] != "v0.3.3" || s.Lines[1].Args["file"] != "cron-amd64.raw" {
		t.Errorf("image line %+v", s.Lines[1])
	}
	if s.Lines[3].Args["done"] != fmt.Sprint(len(img)) {
		t.Errorf("download bar %+v, want %d bytes", s.Lines[3], len(img))
	}
}

// With a StreamRunner the output of zpkg is in the log while zpkg still runs.
func TestInstallStreamsCommandOutputWhileItRuns(t *testing.T) {
	c, g, rec := setup(t)
	c.Progress = &progress.Log{}
	img := "squashfs-cron-v2"
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": img, "cron-amd64.raw.sha256": sum(img)}
	var seenWhileRunning []string
	c.Stream = func(ctx context.Context, out io.Writer, name string, args ...string) error {
		if name == "zpkg" && args[0] == "install" {
			fmt.Fprint(out, "Installing cron\n")
			for _, l := range c.Progress.Snapshot().Lines {
				seenWhileRunning = append(seenWhileRunning, l.Text)
			}
			fmt.Fprint(out, "merged") // no newline at the end
		}
		_, err := rec.run(ctx, name, args...)
		return err
	}
	c.Progress.Start("install", "gh:lintux/cron")
	must(t, c.Install(context.Background(), "gh:lintux/cron"))
	if len(seenWhileRunning) == 0 || seenWhileRunning[len(seenWhileRunning)-1] != "Installing cron" {
		t.Fatalf("zpkg's first line not in the log while it ran: %v", seenWhileRunning)
	}
	got := strings.Join(lines(c.Progress), "|")
	if !strings.Contains(got, "|out Installing cron|out merged|cmd systemctl daemon-reload") {
		t.Fatalf("progress %s", got)
	}
}

// A failed install shows zpkg's error and the way back.
func TestFailedInstallReportsTheWayBack(t *testing.T) {
	c, g, rec := setup(t)
	c.Progress = &progress.Log{}
	img := "squashfs-broken"
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": img, "cron-amd64.raw.sha256": sum(img)}
	rec.failInstall = map[string]bool{img: true}
	must(t, os.WriteFile(filepath.Join(rec.ext, "cron.raw"), []byte("squashfs-cron-v1"), 0o644))
	c.Progress.Start("install", "gh:lintux/cron")
	err := c.Install(context.Background(), "gh:lintux/cron")
	c.Progress.Finish(err)
	if err == nil {
		t.Fatal("install of a broken image succeeded")
	}
	got := strings.Join(lines(c.Progress), "|")
	if !strings.Contains(got, "|out install failed|err wayBack|cmd zpkg install --force ") || !strings.HasSuffix(got, "|err failed") {
		t.Fatalf("progress %s", got)
	}
	if s := c.Progress.Snapshot(); s.OK || s.Running {
		t.Errorf("job %+v", s)
	}
}

// A refused image never reaches zpkg; the log ends with the reason.
func TestRefusedChecksumEndsTheLogWithTheReason(t *testing.T) {
	c, g, _ := setup(t)
	c.Progress = &progress.Log{}
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": "tampered", "cron-amd64.raw.sha256": sum("original")}
	c.Progress.Start("install", "gh:lintux/cron")
	err := c.Install(context.Background(), "gh:lintux/cron")
	c.Progress.Finish(err)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err %v", err)
	}
	got := strings.Join(lines(c.Progress), "|")
	if strings.Contains(got, "ok checksum") || strings.Contains(got, "cmd zpkg") || !strings.HasSuffix(got, "|bar download|err failed") {
		t.Fatalf("progress %s", got)
	}
}

// A download in many pieces within 250 ms: the bar must still end at the full size.
func TestTheDownloadBarEndsAtTheFullSize(t *testing.T) {
	c, g, _ := setup(t)
	c.Progress = &progress.Log{}
	img := strings.Repeat("squashfs", 64<<10) // 512 KiB: io.Copy needs many reads
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": img, "cron-amd64.raw.sha256": sum(img)}
	c.Progress.Start("install", "gh:lintux/cron")
	must(t, c.Install(context.Background(), "gh:lintux/cron"))
	for _, l := range c.Progress.Snapshot().Lines {
		if l.Kind == progress.Bar {
			if l.Args["done"] != fmt.Sprint(len(img)) {
				t.Fatalf("bar ends at %s bytes, want %d", l.Args["done"], len(img))
			}
			return
		}
	}
	t.Fatal("no download bar")
}
