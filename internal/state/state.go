// Package state keeps Module Manager's state directory: what root installs and
// runs (downloads, the self-update script and the image it swaps in), the added
// repositories, the saved manifests of hidden apps and the user.db backups.
//
// Up to 0.3.0 it was /DATA/AppData/modmanager. /DATA/AppData is 0777 without the
// sticky bit on ZimaOS 1.8 (measured on .184, 2026-10-26: a non-root user created a
// directory there, and in /DATA not), and the share of /DATA is writable over SMB:
// whoever can write there can rename the directory and put their own in its place —
// an image swapped between checksum and zpkg install, a symlink root writes through.
// The state now lives in a directory only root can write to, in a parent nobody
// else can write to either (/DATA, 0755 root on 1.8; /var is tmpfs, KB §79.5).
package state

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Dir is the state directory, OldDir the one of 0.3.0 and before.
const (
	Dir    = "/DATA/.modmanager"
	OldDir = "/DATA/AppData/modmanager"
)

// owner is the uid the directories must belong to: root in the service, the
// test's own user in tests.
var owner = os.Geteuid()

func ownedBy(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == owner
}

func uidOf(fi os.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// Ensure makes dir the state directory: a real directory (no symlink), owned by
// this process' user, mode 0700, in a parent owned by root or this user that
// neither group nor others can write to.
func Ensure(dir string) error {
	parent := filepath.Dir(dir)
	pfi, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !pfi.IsDir() {
		return fmt.Errorf("state directory: %s is not a directory", parent)
	}
	if u := uidOf(pfi); u != 0 && u != owner {
		return fmt.Errorf("state directory: %s belongs to uid %d", parent, u)
	}
	if pfi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("state directory: others can write to %s (mode %v)", parent, pfi.Mode().Perm())
	}
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
		fi, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("state directory: %s is not a plain directory", dir)
	}
	if !ownedBy(fi) {
		return fmt.Errorf("state directory: %s belongs to uid %d", dir, uidOf(fi))
	}
	if fi.Mode().Perm() != 0o700 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// migrated marks a done migration: the old place is never read again.
const migrated = ".migrated-from-appdata"

// Migrate copies the state of 0.3.0 and before from oldDir into dir, once: the
// added repositories, the language, the manifests of hidden apps and the user.db
// backups. Downloads, caches and the self-update are not taken. Only plain files
// owned by this user come over (no symlinks: the old place may have been changed by
// others). The old directory stays (deleting is the user's call). It returns the
// files taken, relative to dir, and an error naming what was skipped.
func Migrate(oldDir, dir string) ([]string, error) {
	mark := filepath.Join(dir, migrated)
	if _, err := os.Lstat(mark); err == nil {
		return nil, nil
	}
	var taken, skipped []string
	ofi, err := os.Lstat(oldDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	case ofi.Mode()&os.ModeSymlink != 0 || !ofi.IsDir() || !ownedBy(ofi):
		skipped = append(skipped, oldDir+" (not a plain directory of this user)")
	default:
		for _, f := range []string{"sources.json", "ui-lang"} {
			if ok, why := copyPlain(filepath.Join(oldDir, f), filepath.Join(dir, f)); ok {
				taken = append(taken, f)
			} else if why != "" {
				skipped = append(skipped, f+" ("+why+")")
			}
		}
		for _, sub := range []string{"manifests", "userdb-backups"} {
			sfi, err := os.Lstat(filepath.Join(oldDir, sub))
			if err != nil {
				continue
			}
			if sfi.Mode()&os.ModeSymlink != 0 || !sfi.IsDir() || !ownedBy(sfi) {
				skipped = append(skipped, sub+" (not a plain directory of this user)")
				continue
			}
			entries, err := os.ReadDir(filepath.Join(oldDir, sub))
			if err != nil {
				return taken, err
			}
			for _, e := range entries {
				rel := filepath.Join(sub, e.Name())
				if ok, why := copyPlain(filepath.Join(oldDir, rel), filepath.Join(dir, rel)); ok {
					taken = append(taken, rel)
				} else if why != "" {
					skipped = append(skipped, rel+" ("+why+")")
				}
			}
		}
	}
	if err := os.WriteFile(mark, []byte(oldDir+"\n"), 0o600); err != nil {
		return taken, err
	}
	if len(skipped) > 0 {
		return taken, fmt.Errorf("not taken from %s: %v", oldDir, skipped)
	}
	return taken, nil
}

// copyPlain copies src to dst (mode 0600) when src is a plain file of this user.
// A missing src is no reason ("").
func copyPlain(src, dst string) (bool, string) {
	fi, err := os.Lstat(src)
	if errors.Is(err, os.ErrNotExist) {
		return false, ""
	}
	if err != nil {
		return false, err.Error()
	}
	if !fi.Mode().IsRegular() || !ownedBy(fi) {
		return false, "not a plain file of this user"
	}
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false, err.Error()
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return false, err.Error()
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return false, err.Error()
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return false, err.Error()
	}
	if err := out.Close(); err != nil {
		return false, err.Error()
	}
	return true, ""
}
