package state

import (
	"os"
	"path/filepath"
	"testing"
)

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode()
}

// The state directory holds what root installs and runs (downloads, the self-update
// script): it is made 0700 in a parent nobody else can write to.
func TestEnsureMakesARootOnlyDirectory(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, ".modmanager")
	if err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, dir); !m.IsDir() || m.Perm() != 0o700 {
		t.Fatalf("mode %v", m)
	}
	// an existing directory with looser rights is tightened
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, dir); m.Perm() != 0o700 {
		t.Fatalf("not tightened: %v", m)
	}
}

// /DATA/AppData is 0777 without the sticky bit on ZimaOS 1.8 (measured on .184,
// 2026-10-26): anybody who can write there can swap a directory for their own.
func TestEnsureRefusesAParentOthersCanWrite(t *testing.T) {
	for _, m := range []os.FileMode{0o777, 0o775, 0o757, 0o1777} {
		parent := t.TempDir()
		if err := os.Chmod(parent, m); err != nil {
			t.Fatal(err)
		}
		if err := Ensure(filepath.Join(parent, ".modmanager")); err == nil {
			t.Errorf("parent %v accepted", m)
		}
	}
}

func TestEnsureRefusesASymlink(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(parent, ".modmanager")); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(filepath.Join(parent, ".modmanager")); err == nil {
		t.Fatal("a symlink as state directory accepted")
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The state of 0.3.0 and before (/DATA/AppData/modmanager) comes over once: the
// added repositories, the language, the saved manifests of hidden apps (Restore
// needs them) and the user.db backups — not downloads, caches or the self-update.
func TestMigrateTakesTheStateOnce(t *testing.T) {
	old := filepath.Join(t.TempDir(), "modmanager")
	write(t, filepath.Join(old, "sources.json"), `["a/b"]`)
	write(t, filepath.Join(old, "ui-lang"), "fr\n")
	write(t, filepath.Join(old, "manifests", "icewhale_photos.json"), `{"services":["x"]}`)
	write(t, filepath.Join(old, "userdb-backups", "user.db.20261026"), "db")
	write(t, filepath.Join(old, "downloads", "cron.raw"), "img")
	write(t, filepath.Join(old, "release-cache.json"), "{}")
	write(t, filepath.Join(old, "self-update", "run.sh"), "x")
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, ".modmanager")
	if err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	got, err := Migrate(old, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("migrated %v, want 4 files", got)
	}
	for _, f := range []string{"sources.json", "ui-lang", "manifests/icewhale_photos.json", "userdb-backups/user.db.20261026"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if m := mode(t, filepath.Join(dir, f)); m.Perm() != 0o600 {
			t.Errorf("%s mode %v", f, m)
		}
		_ = b
	}
	for _, f := range []string{"downloads", "release-cache.json", "self-update"} {
		if _, err := os.Lstat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s came over", f)
		}
	}
	// once: a later change in the old place is not taken again
	write(t, filepath.Join(old, "sources.json"), `["evil/x"]`)
	if again, err := Migrate(old, dir); err != nil || len(again) != 0 {
		t.Fatalf("second migrate %v %v", again, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "sources.json")); string(b) != `["a/b"]` {
		t.Fatalf("sources.json %q", b)
	}
}

// A symlink in the old place (planted by whoever could write /DATA/AppData) is not
// followed: neither as the old directory nor as a file in it.
func TestMigrateFollowsNoSymlink(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret")
	write(t, secret, "do not copy")
	base := t.TempDir()
	old := filepath.Join(base, "modmanager")
	write(t, filepath.Join(old, "ui-lang"), "de\n")
	if err := os.Symlink(secret, filepath.Join(old, "sources.json")); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, ".modmanager")
	if err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	got, err := Migrate(old, dir)
	if err == nil {
		t.Error("the skipped symlink is not reported")
	}
	if len(got) != 1 || got[0] != "ui-lang" {
		t.Fatalf("migrated %v", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, "sources.json")); err == nil {
		t.Fatal("the symlink's target came over")
	}

	// the old directory itself a symlink: nothing comes over
	linked := filepath.Join(base, "linked")
	if err := os.Symlink(old, linked); err != nil {
		t.Fatal(err)
	}
	dir2 := filepath.Join(parent, ".modmanager2")
	if err := Ensure(dir2); err != nil {
		t.Fatal(err)
	}
	if got, err := Migrate(linked, dir2); err == nil || len(got) != 0 {
		t.Fatalf("symlinked old dir: %v %v", got, err)
	}
}

// Nothing to migrate (a fresh install) is no error.
func TestMigrateWithoutOldState(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, ".modmanager")
	if err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if got, err := Migrate(filepath.Join(parent, "none"), dir); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}
