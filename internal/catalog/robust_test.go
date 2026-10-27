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

// An update swaps the image with zpkg install --force: when that fails, the previous
// image goes back the same way, so a failed update leaves the module as it was.
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

// One refresh instead of two (KB §79.15): an update never runs zpkg remove, and it does
// to the services what remove + install did — stop what only the old image had, restart
// what runs (try-restart leaves a service the user turned off alone), start the services
// of the module's manifest.
func TestAnUpdateSwapsWithOneRefreshAndRestartsTheServices(t *testing.T) {
	c, g, rec := setup(t)
	old, img := "squashfs-cron-v1", "squashfs-cron-v2"
	rec.lists = map[string]string{
		old: "squashfs-root/usr/lib/systemd/system/cron.service\nsquashfs-root/usr/lib/systemd/system/cron-old.service\nsquashfs-root/usr/lib/systemd/system/cron-job@.service\n",
		img: "squashfs-root/usr/lib/systemd/system/cron.service\nsquashfs-root/usr/lib/systemd/system/cron-job@.service\nsquashfs-root/usr/lib/systemd/system/cron-web.service\n",
	}
	rec.instances = map[string]string{"cron-job@*.service": "cron-job@daily.service loaded active running Job\n"}
	must(t, os.WriteFile(filepath.Join(rec.ext, "cron.raw"), []byte(old), 0o644))
	must(t, os.MkdirAll(filepath.Join(c.Root, "usr/share/casaos/modules"), 0o755))
	must(t, os.WriteFile(filepath.Join(c.Root, "usr/share/casaos/modules/cron.json"), []byte(`{"name":"cron","services":[{"name":"cron"},{"name":"bad;name"}]}`), 0o644))
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": img, "cron-amd64.raw.sha256": sum(img)}
	must(t, c.Install(context.Background(), "gh:lintux/cron"))
	if rec.has("zpkg remove") {
		t.Fatalf("update ran zpkg remove (a second refresh): %v", rec.cmds)
	}
	var tail []string
	for _, cmd := range rec.cmds {
		if strings.HasPrefix(cmd, "zpkg") || strings.HasPrefix(cmd, "systemctl stop") || strings.HasPrefix(cmd, "systemctl try-restart") || strings.HasPrefix(cmd, "systemctl start") {
			tail = append(tail, cmd)
		}
	}
	want := []string{
		"zpkg install --force " + filepath.Join(c.StateDir, "downloads", "cron.raw"),
		"systemctl stop cron-old.service",
		"systemctl try-restart cron.service cron-job@daily.service cron-web.service",
		"systemctl start cron.service",
	}
	if strings.Join(tail, "|") != strings.Join(want, "|") {
		t.Fatalf("commands\n got %v\nwant %v", tail, want)
	}
	if b, _ := os.ReadFile(filepath.Join(rec.ext, "cron.raw")); string(b) != img {
		t.Errorf("installed %q, want the new image", b)
	}
}

// A first install is one plain zpkg install: nothing to restart, no --force.
func TestAFirstInstallIsOnePlainInstall(t *testing.T) {
	c, g, rec := setup(t)
	g.releases["lintux/cron"] = map[string]string{"cron-amd64.raw": "img", "cron-amd64.raw.sha256": sum("img")}
	must(t, c.Install(context.Background(), "gh:lintux/cron"))
	if !rec.has("zpkg install "+filepath.Join(c.StateDir, "downloads", "cron.raw")) || rec.has("zpkg install --force") || rec.has("systemctl try-restart") {
		t.Fatalf("commands %v", rec.cmds)
	}
}
