package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SelfRepo is where Module Manager's own releases come from.
const SelfRepo = "chicohaager/modmanager"

// ErrNotNewer: the latest release is not newer than the running version.
var ErrNotNewer = errors.New("the latest release is not newer than this Module Manager")

// SelfStatus is what the UI shows about updating Module Manager itself.
type SelfStatus struct {
	Installed string `json:"installed"`
	Latest    string `json:"latest,omitempty"`
	Update    bool   `json:"update"`
	Note      string `json:"note,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Self reports whether a newer release of Module Manager exists.
func (c *Catalog) Self(ctx context.Context, installed string) SelfStatus {
	st := SelfStatus{Installed: installed}
	rel, stale, err := c.latest(ctx, SelfRepo, false)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Latest = rel.Tag
	st.Update = CompareVersions(installed, rel.Tag) < 0
	if !stale.IsZero() {
		st.Note = "release info from " + stale.Local().Format("2006-01-02 15:04")
	}
	return st
}

func (c *Catalog) selfDir() string { return filepath.Join(c.StateDir, "self-update") }

// selfScript runs in a transient unit (systemd-run), outside Module Manager's own
// service, which it restarts. It swaps the image with one `zpkg install --force` (one
// systemd-sysext refresh; remove + install were two, and every refresh leaves its loop
// devices behind while processes of the old /usr run, KB §79.15). --force does not
// restart the service (KB §79.13), so the script does. A failed swap puts the previous
// image back the same way; the last line of the log is the result: ok, rolled-back or
// rollback-failed. E is kept for the message of a failed way back.
const selfScript = `#!/bin/sh
# Written by Module Manager: replaces itself with the new image, rolls back on failure.
D=%q
E=%q
exec >"$D/log" 2>&1
sleep 2
echo "swap $(date)"
if zpkg install --force "$D/new/modmanager.raw"; then
  systemctl daemon-reload
  systemctl restart modmanager.service
  rm -f "$D/new/modmanager.raw"  # zpkg copied it to /var/lib/extensions; previous/ stays as the way back
  echo "result ok"
else
  echo "install of the new image failed, rolling back"
  if zpkg install --force "$D/previous/modmanager.raw"; then
    systemctl daemon-reload
    systemctl restart modmanager.service
    echo "result rolled-back"
  else
    echo "result rollback-failed: install $D/previous/modmanager.raw by hand (zpkg install --force, then systemctl restart modmanager); installed now: $E/modmanager.raw"
    exit 1
  fi
fi
`

// SelfUpdate downloads the newest release (checksum and collision checked like any
// catalog install), keeps a copy of the installed image for the rollback and hands the
// swap to a transient unit. It returns the release tag; the UI then waits until
// /modmanager/health answers with the new version.
func (c *Catalog) SelfUpdate(ctx context.Context, installed string) (string, error) {
	c.Progress.Stepf("release", "Asking GitHub for the latest release of {repo}", "repo", SelfRepo)
	rel, _, err := c.latest(ctx, SelfRepo, true)
	if err != nil {
		return "", err
	}
	if CompareVersions(installed, rel.Tag) >= 0 {
		return "", ErrNotNewer
	}
	name, img, err := c.pick(rel)
	if err != nil {
		return "", err
	}
	if name != "modmanager" {
		return "", fmt.Errorf("release image %s is not Module Manager", img.Name)
	}
	c.Progress.Stepf("image", "Release {tag}: image {file}", "tag", rel.Tag, "file", img.Name)
	want, err := c.checksumFor(ctx, rel, img)
	if err != nil {
		return "", err
	}
	dir := c.selfDir()
	for _, sub := range []string{"new", "previous"} {
		if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return "", err
		}
	}
	newRaw := filepath.Join(dir, "new", "modmanager.raw") // zpkg takes the module name from the file name
	if err := c.download(ctx, img.URL, newRaw, want); err != nil {
		return "", err
	}
	if err := c.checkCollisions(ctx, name, newRaw); err != nil {
		return "", err
	}
	c.Progress.Stepf("keep", "Keeping a copy of the installed image for the way back")
	if err := copyFile(c.installedFile(name), filepath.Join(dir, "previous", "modmanager.raw")); err != nil {
		return "", fmt.Errorf("no copy of the installed image for a rollback: %w", err)
	}
	script := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(script, []byte(fmt.Sprintf(selfScript, dir, c.ExtDir)), 0o700); err != nil {
		return "", err
	}
	_ = os.Remove(filepath.Join(dir, "log"))
	c.Progress.Stepf("handover", "Handing the swap to a separate unit: Module Manager is away for a few seconds")
	if out, err := c.change(ctx, "systemd-run", "--unit=modmanager-self-update", "--collect", "--no-block", "/bin/sh", script); err != nil {
		return "", fmt.Errorf("systemd-run: %v %s", err, strings.TrimSpace(string(out)))
	}
	return rel.Tag, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
