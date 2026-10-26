package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// build-sysext.sh writes <name>.raw only for amd64, as a copy of <name>-amd64.raw
// (lintux-modkit sysext/build-sysext.sh): on arm64 that arch-less file is the
// amd64 image and must not be taken.
func TestArm64DoesNotTakeTheAmd64Alias(t *testing.T) {
	c := &Catalog{Arch: "arm64"}
	cases := []struct {
		assets      []string
		name, asset string
		err         error
	}{
		{[]string{"cron-amd64.raw", "cron.raw"}, "", "", ErrNoImage},
		{[]string{"cron-amd64.raw", "cron-arm64.raw", "cron.raw"}, "cron", "cron-arm64.raw", nil},
		{[]string{"mergerfs-snapraid_amd64.raw", "mergerfs-snapraid.raw"}, "", "", ErrNoImage},
		// a release with only an arch-less image says nothing about its CPU: as before
		{[]string{"web-ftp-client.raw"}, "web-ftp-client", "web-ftp-client.raw", nil},
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

// An update removes the installed module before installing the new image (zpkg
// refuses a second image of the same name): when that install fails, the previous
// image goes back, so a failed update never leaves the module removed.
func TestAFailedUpdatePutsThePreviousImageBack(t *testing.T) {
	c, g, rec := setup(t)
	old, img := "squashfs-cron-v1", "squashfs-cron-v2"
	must(t, os.WriteFile(filepath.Join(rec.ext, "cron.raw"), []byte(old), 0o644))
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": img, "cron-amd64.raw.sha256": sum(img)}
	rec.failInstall = map[string]bool{img: true}
	err := c.Install(context.Background(), "gh:lintux/cron")
	if err == nil || !strings.Contains(err.Error(), "previous version installed again") {
		t.Fatalf("err %v, want the failure and that the previous version is back", err)
	}
	b, rerr := os.ReadFile(filepath.Join(rec.ext, "cron.raw"))
	if rerr != nil || string(b) != old {
		t.Fatalf("installed afterwards: %q %v, want the previous image", b, rerr)
	}
	// the copy for the way back does not stay behind
	left, _ := filepath.Glob(filepath.Join(c.StateDir, "downloads", "*"))
	if len(left) != 0 {
		t.Errorf("left in downloads: %v", left)
	}
}

// When even the way back fails, the error says so: the module is not installed.
func TestAFailedRollbackIsReported(t *testing.T) {
	c, g, rec := setup(t)
	old, img := "squashfs-cron-v1", "squashfs-cron-v2"
	must(t, os.WriteFile(filepath.Join(rec.ext, "cron.raw"), []byte(old), 0o644))
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": img, "cron-amd64.raw.sha256": sum(img)}
	rec.failInstall = map[string]bool{img: true, old: true}
	err := c.Install(context.Background(), "gh:lintux/cron")
	if err == nil || !strings.Contains(err.Error(), "previous version could not be installed again") {
		t.Fatalf("err %v", err)
	}
}

// A request the user cancelled (closed tab) or that ran out of time says nothing
// about GitHub: its error must not be served from the cache to the next request.
func TestACanceledFetchIsNotCached(t *testing.T) {
	c, g, _ := setup(t)
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": "x", "cron-amd64.raw.sha256": sum("x")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.latest(ctx, "lintux/cron", false); err == nil {
		t.Fatal("a canceled request must fail")
	}
	if _, _, err := c.latest(context.Background(), "lintux/cron", false); err != nil {
		t.Fatalf("the next request got the canceled one's error: %v", err)
	}
}
