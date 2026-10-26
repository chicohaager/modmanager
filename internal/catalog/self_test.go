package catalog

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func selfRelease(t *testing.T, tag string) (*Catalog, *fakeGitHub, *recorder) {
	t.Helper()
	c, g, rec := setup(t)
	g.releases[SelfRepo] = map[string]string{"modmanager-amd64.raw": "new image", "modmanager-amd64.raw.sha256": sum("new image")}
	g.tags[SelfRepo] = tag
	must(t, os.WriteFile(c.installedFile("modmanager"), []byte("old image"), 0o644))
	return c, g, rec
}

func TestSelfReportsANewerRelease(t *testing.T) {
	c, _, _ := selfRelease(t, "v0.3.0")
	st := c.Self(context.Background(), "0.2.5")
	if !st.Update || st.Latest != "v0.3.0" || st.Error != "" {
		t.Fatalf("want an update to v0.3.0, got %+v", st)
	}
	if st := c.Self(context.Background(), "0.3.0"); st.Update {
		t.Fatalf("same version offered as update: %+v", st)
	}
}

func TestSelfUpdateRefusesWhenNotNewer(t *testing.T) {
	c, _, rec := selfRelease(t, "v0.2.5")
	if _, err := c.SelfUpdate(context.Background(), "0.2.5"); !errors.Is(err, ErrNotNewer) {
		t.Fatalf("want ErrNotNewer, got %v", err)
	}
	if rec.has("systemd-run") {
		t.Fatal("handed over although not newer")
	}
}

// The update stages the verified image and a copy of the installed one, then hands over to a
// transient unit: Module Manager removes itself from /usr during the swap and must not do it in-process.
func TestSelfUpdateStagesAndHandsOver(t *testing.T) {
	c, _, rec := selfRelease(t, "v0.3.0")
	tag, err := c.SelfUpdate(context.Background(), "0.2.5")
	must(t, err)
	if tag != "v0.3.0" {
		t.Fatalf("tag %q", tag)
	}
	dir := filepath.Join(c.StateDir, "self-update")
	for f, want := range map[string]string{"new/modmanager.raw": "new image", "previous/modmanager.raw": "old image"} {
		if b, err := os.ReadFile(filepath.Join(dir, f)); err != nil || string(b) != want {
			t.Fatalf("%s: %q %v, want %q", f, b, err, want)
		}
	}
	if !rec.has("systemd-run --unit=modmanager-self-update --collect --no-block /bin/sh " + filepath.Join(dir, "run.sh")) {
		t.Fatalf("no hand-over to a transient unit: %v", rec.cmds)
	}
}

func TestSelfUpdateRefusesABadChecksum(t *testing.T) {
	c, g, rec := selfRelease(t, "v0.3.0")
	g.releases[SelfRepo]["modmanager-amd64.raw.sha256"] = sum("something else")
	if _, err := c.SelfUpdate(context.Background(), "0.2.5"); !errors.Is(err, ErrChecksum) {
		t.Fatalf("want ErrChecksum, got %v", err)
	}
	if rec.has("systemd-run") {
		t.Fatal("handed over a download that failed its checksum")
	}
}

// The hand-over script with a fake zpkg: a failing install of the new image puts the previous one back.
func TestSelfUpdateScriptRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name, failOn, result, installedLast string
	}{
		{"new installs", "", "result ok", "new/modmanager.raw"},
		{"new fails", "*new/modmanager.raw*", "result rolled-back", "previous/modmanager.raw"},
		// remove failed: the installed version stays, nothing is installed over it
		{"remove fails", `*"remove modmanager"*`, "result remove-failed", ""},
		// the way back failed too: the log must not claim a rollback
		{"rollback fails", "*new/modmanager.raw*|*previous/modmanager.raw*", "result rollback-failed", "previous/modmanager.raw"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := selfRelease(t, "v0.3.0")
			_, err := c.SelfUpdate(context.Background(), "0.2.5")
			must(t, err)
			dir := filepath.Join(c.StateDir, "self-update")
			bin := t.TempDir()
			log := filepath.Join(bin, "calls")
			fake := "#!/bin/sh\necho \"$(basename \"$0\") $*\" >> " + log + "\n"
			if tc.failOn != "" {
				fake += "case \"$*\" in " + tc.failOn + ") exit 1 ;; esac\n"
			}
			for _, n := range []string{"zpkg", "systemctl", "sleep"} {
				must(t, os.WriteFile(filepath.Join(bin, n), []byte(fake), 0o755))
			}
			cmd := exec.Command("/bin/sh", filepath.Join(dir, "run.sh"))
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			// the transient unit fails (and shows as failed) when Module Manager may be gone
			sout, err := cmd.CombinedOutput()
			wantFail := tc.result == "result remove-failed" || tc.result == "result rollback-failed"
			if (err != nil) != wantFail {
				t.Fatalf("script: err %v (want failure %v) %s", err, wantFail, sout)
			}
			out, _ := os.ReadFile(filepath.Join(dir, "log"))
			calls, _ := os.ReadFile(log)
			if !strings.Contains(string(out), tc.result) {
				t.Fatalf("log %q, want %q", out, tc.result)
			}
			lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
			var lastInstall string
			for _, l := range lines {
				if strings.HasPrefix(l, "zpkg install ") {
					lastInstall = l
				}
			}
			if tc.installedLast == "" {
				if lastInstall != "" {
					t.Fatalf("calls:\n%s\nwant no install after a failed remove", calls)
				}
			} else if !strings.HasSuffix(lastInstall, tc.installedLast) || !strings.Contains(string(calls), "zpkg remove modmanager") {
				t.Fatalf("calls:\n%s\nwant remove and last install of %s", calls, tc.installedLast)
			}
			// zpkg copied the new image to /var/lib/extensions (measured .184): after success the staged copy goes,
			// the previous one stays as the way back; after a rollback both stay for a look.
			_, errNew := os.Stat(filepath.Join(dir, "new", "modmanager.raw"))
			if gone := os.IsNotExist(errNew); gone != (tc.result == "result ok") {
				t.Fatalf("staged new image gone=%v after %q", gone, tc.result)
			}
			if _, err := os.Stat(filepath.Join(dir, "previous", "modmanager.raw")); err != nil {
				t.Fatalf("previous image: %v", err)
			}
		})
	}
}
