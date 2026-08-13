package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathUserScope(t *testing.T) {
	home := t.TempDir()
	cases := map[string]string{
		"claude": filepath.Join(home, ".claude", "skills", "localapp", "SKILL.md"),
		"codex":  filepath.Join(home, ".codex", "skills", "localapp", "SKILL.md"),
	}
	for target, want := range cases {
		got, err := Path(target, Scope{Home: home})
		if err != nil {
			t.Fatalf("Path(%q): %v", target, err)
		}
		if got != want {
			t.Errorf("Path(%q) = %s, want %s", target, got, want)
		}
	}
}

func TestPathProjectScope(t *testing.T) {
	cwd := t.TempDir()
	got, err := Path("claude", Scope{Project: true, Cwd: cwd})
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	want := filepath.Join(cwd, ".claude", "skills", "localapp", "SKILL.md")
	if got != want {
		t.Errorf("Path = %s, want %s", got, want)
	}
}

// TestPathDirOverride checks that --dir is treated as the skills root.
func TestPathDirOverride(t *testing.T) {
	dir := t.TempDir()
	// It resolves even without a target name.
	got, err := Path("", Scope{Dir: dir, Home: "/should/not/be/used"})
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	want := filepath.Join(dir, "localapp", "SKILL.md")
	if got != want {
		t.Errorf("Path = %s, want %s", got, want)
	}
}

func TestPathUnknownTarget(t *testing.T) {
	if _, err := Path("cursor", Scope{Home: t.TempDir()}); err == nil {
		t.Fatal("an unknown target did not produce an error")
	}
}

// TestPathUsesHomeEnv checks that os.UserHomeDir() (= $HOME) is used when
// Home is not set.
func TestPathUsesHomeEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := Path("claude", Scope{})
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	want := filepath.Join(home, ".claude", "skills", "localapp", "SKILL.md")
	if got != want {
		t.Errorf("Path = %s, want %s", got, want)
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	home := t.TempDir()
	sc := Scope{Home: home}

	path, err := Install("v1", "claude", sc)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	want := filepath.Join(home, ".claude", "skills", "localapp", "SKILL.md")
	if path != want {
		t.Fatalf("destination = %s, want %s", path, want)
	}
	assertContent(t, path, "v1")

	// Re-running replaces it with the latest version.
	if _, err := Install("v2", "claude", sc); err != nil {
		t.Fatalf("Install (second call): %v", err)
	}
	assertContent(t, path, "v2")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("permissions = %o, want 644", perm)
	}
}

func TestUninstallRemovesFileAndEmptyDir(t *testing.T) {
	home := t.TempDir()
	sc := Scope{Home: home}
	path, err := Install("body", "claude", sc)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	got, removed, err := Uninstall("claude", sc)
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !removed || got != path {
		t.Fatalf("Uninstall = (%s, %v), want (%s, true)", got, removed, path)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("SKILL.md is still present: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("the empty localapp directory is still present")
	}
	// The parent directory is kept because other skills may live there.
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); err != nil {
		t.Errorf("the skills directory was deleted: %v", err)
	}
}

// TestUninstallKeepsNonEmptyDir checks that the localapp directory is kept
// when other files remain in it.
func TestUninstallKeepsNonEmptyDir(t *testing.T) {
	home := t.TempDir()
	sc := Scope{Home: home}
	path, err := Install("body", "claude", sc)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	extra := filepath.Join(filepath.Dir(path), "reference.md")
	if err := os.WriteFile(extra, []byte("x"), 0o644); err != nil {
		t.Fatalf("creating the extra file: %v", err)
	}

	if _, _, err := Uninstall("claude", sc); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(extra); err != nil {
		t.Errorf("the co-located file was deleted: %v", err)
	}
}

// TestUninstallMissingIsNoop checks that uninstalling when nothing is
// installed is not an error.
func TestUninstallMissingIsNoop(t *testing.T) {
	_, removed, err := Uninstall("codex", Scope{Home: t.TempDir()})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if removed {
		t.Error("removed = true, want false")
	}
}

func TestTargets(t *testing.T) {
	got := Targets()
	if len(got) != 2 || got[0] != "claude" || got[1] != "codex" {
		t.Errorf("Targets() = %v, want [claude codex]", got)
	}
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the installed file: %v", err)
	}
	if string(data) != want {
		t.Errorf("content = %q, want %q", data, want)
	}
}
