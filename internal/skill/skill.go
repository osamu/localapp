// Package skill installs and removes the embedded SKILL.md
// (DESIGN.md "Coding Agent 向け SKILL 仕様", distribution).
//
// There are three destinations: user scope
// (`~/.claude/skills/localapp/SKILL.md`), project scope
// (`.claude/skills/localapp/SKILL.md` relative to the cwd), and an arbitrary
// location given by `--dir`. Writes happen only in the user's own area, so no
// sudo is required.
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileName is the name of the installed file.
const FileName = "SKILL.md"

// DirName is the skill directory name. The destination is
// `<skills root>/<DirName>/<FileName>`.
const DirName = "localapp"

// targetRoots maps a target name to the agent's configuration directory name.
var targetRoots = map[string]string{
	"claude": ".claude",
	"codex":  ".codex",
}

// Targets returns the target names that may be specified.
func Targets() []string {
	out := make([]string, 0, len(targetRoots))
	for name := range targetRoots {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Scope decides where the file is installed.
type Scope struct {
	// Project, when true, installs relative to the cwd
	// (`.claude/skills/...`).
	Project bool
	// Dir names the skills directory directly. When set, the file goes to
	// `<Dir>/localapp/SKILL.md` and Target and Project are ignored.
	Dir string
	// Home / Cwd are substitution points for the base directories. When empty,
	// os.UserHomeDir() / os.Getwd() are used.
	Home string
	Cwd  string
}

// Path returns the absolute path of the installed SKILL.md.
func Path(target string, sc Scope) (string, error) {
	if sc.Dir != "" {
		dir, err := abs(sc.Dir)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, DirName, FileName), nil
	}
	root, ok := targetRoots[target]
	if !ok {
		return "", fmt.Errorf("unknown target: %q (use one of %s, or pass --dir)",
			target, strings.Join(Targets(), " / "))
	}
	base, err := sc.base()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, root, "skills", DirName, FileName), nil
}

// base returns the base directory of the user or project scope.
func (sc Scope) base() (string, error) {
	if sc.Project {
		if sc.Cwd != "" {
			return abs(sc.Cwd)
		}
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getting the current directory: %w", err)
		}
		return cwd, nil
	}
	if sc.Home != "" {
		return abs(sc.Home)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting the home directory: %w", err)
	}
	return home, nil
}

func abs(p string) (string, error) {
	a, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolving the path (%s): %w", p, err)
	}
	return a, nil
}

// Install writes content to the destination and returns its path.
// An existing file is overwritten, so re-running is idempotent.
func Install(content, target string, sc Scope) (string, error) {
	path, err := Path(target, sc)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating the skill directory (%s): %w", dir, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("writing SKILL.md (%s): %w", path, err)
	}
	return path, nil
}

// Uninstall removes an installed SKILL.md, along with the localapp directory
// once it is empty. When the file was not there to begin with it returns
// removed=false rather than an error (idempotent).
func Uninstall(target string, sc Scope) (path string, removed bool, err error) {
	path, err = Path(target, sc)
	if err != nil {
		return "", false, err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return path, false, nil
		}
		return path, false, fmt.Errorf("removing SKILL.md (%s): %w", path, err)
	}
	// Remove only the localapp directory once it is empty. The parents
	// (skills / .claude) are kept because other skills may live there. If it is
	// not empty os.Remove fails, and that error can be ignored.
	_ = os.Remove(filepath.Dir(path))
	return path, true, nil
}
