package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	localapp "github.com/osamu/localapp"
)

// captureStdout captures stdout as a string while fn runs.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating the pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()
	fn()
	os.Stdout = orig
	w.Close()
	out := <-done
	r.Close()
	return out
}

// TestSkillShow checks that the embedded SKILL.md is printed with its
// frontmatter.
func TestSkillShow(t *testing.T) {
	var code int
	out := captureStdout(t, func() { code = run([]string{"skill", "show"}) })
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if out != localapp.SkillMD {
		t.Error("the output does not match the embedded SKILL.md")
	}
	if !strings.HasPrefix(out, "---\nname: localapp\n") {
		t.Errorf("the frontmatter is not at the top:\n%.80s", out)
	}
	if !strings.Contains(out, "\ndescription: ") {
		t.Error("the frontmatter has no description")
	}
}

// TestSkillInstallUninstall replaces HOME to check installing and removing in
// user scope. It never touches the real user's ~/.claude.
func TestSkillInstallUninstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, tc := range []struct{ target, dir string }{
		{"claude", ".claude"},
		{"codex", ".codex"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			want := filepath.Join(home, tc.dir, "skills", "localapp", "SKILL.md")

			out := captureStdout(t, func() {
				if code := run([]string{"skill", "install", tc.target}); code != exitOK {
					t.Fatalf("exit code of install = %d", code)
				}
			})
			if strings.TrimSpace(out) != want {
				t.Errorf("stdout = %q, want %q", strings.TrimSpace(out), want)
			}
			data, err := os.ReadFile(want)
			if err != nil {
				t.Fatalf("reading the installed file: %v", err)
			}
			if string(data) != localapp.SkillMD {
				t.Error("the installed content does not match the embedded SKILL.md")
			}

			// Re-running is idempotent.
			captureStdout(t, func() {
				if code := run([]string{"skill", "install", tc.target}); code != exitOK {
					t.Fatalf("exit code of the second install = %d", code)
				}
			})

			captureStdout(t, func() {
				if code := run([]string{"skill", "uninstall", tc.target}); code != exitOK {
					t.Fatalf("exit code of uninstall = %d", code)
				}
			})
			if _, err := os.Stat(want); !os.IsNotExist(err) {
				t.Errorf("SKILL.md is still present: %v", err)
			}
			if _, err := os.Stat(filepath.Dir(want)); !os.IsNotExist(err) {
				t.Error("the empty localapp directory is still present")
			}
		})
	}
}

// TestSkillInstallProject checks that --project installs relative to the cwd.
func TestSkillInstallProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	t.Chdir(cwd)

	captureStdout(t, func() {
		if code := run([]string{"skill", "install", "claude", "--project"}); code != exitOK {
			t.Fatalf("exit code = %d", code)
		}
	})
	want := filepath.Join(cwd, ".claude", "skills", "localapp", "SKILL.md")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("nothing was installed in project scope: %v", err)
	}
	// Nothing is installed under HOME.
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Error("something was installed under HOME")
	}
}

// TestSkillInstallDir checks that --dir installs into an arbitrary skills
// directory.
func TestSkillInstallDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	// With --dir alone the target name may be omitted.
	captureStdout(t, func() {
		if code := run([]string{"skill", "install", "--dir", dir}); code != exitOK {
			t.Fatalf("exit code = %d", code)
		}
	})
	want := filepath.Join(dir, "localapp", "SKILL.md")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("nothing was installed into --dir: %v", err)
	}

	captureStdout(t, func() {
		if code := run([]string{"skill", "uninstall", "--dir", dir}); code != exitOK {
			t.Fatalf("exit code of uninstall = %d", code)
		}
	})
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Error("the SKILL.md under --dir is still present")
	}
}

// TestSkillUninstallMissingIsOK checks that uninstalling when nothing is
// installed counts as success (idempotent).
func TestSkillUninstallMissingIsOK(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if code := run([]string{"skill", "uninstall", "claude"}); code != exitOK {
		t.Errorf("exit code = %d, want %d", code, exitOK)
	}
}

func TestSkillBadUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tests := [][]string{
		{"skill"},
		{"skill", "nosuch"},
		{"skill", "show", "extra"},
		{"skill", "install"},                    // neither a target nor --dir
		{"skill", "install", "claude", "extra"}, // too many positional arguments
		{"skill", "install", "claude", "--project", "--dir", "/tmp/x"}, // cannot be combined
		{"skill", "uninstall"},
	}
	for _, args := range tests {
		if code := run(args); code != exitUsage {
			t.Errorf("run(%v) = %d, want %d", args, code, exitUsage)
		}
	}
}

// TestSkillInstallUnknownTarget checks that an unknown target exits 1.
func TestSkillInstallUnknownTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if code := run([]string{"skill", "install", "cursor"}); code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
}
