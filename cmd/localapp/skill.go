package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	localapp "github.com/osamu/localapp"
	"github.com/osamu/localapp/internal/skill"
)

// cmdSkill handles `localapp skill show / install / uninstall`
// (DESIGN.md "Agent skill", distribution).
func cmdSkill(args []string) int {
	if len(args) == 0 {
		skillUsage()
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "show":
		return cmdSkillShow(rest)
	case "install":
		return cmdSkillInstall(rest)
	case "uninstall":
		return cmdSkillUninstall(rest)
	case "-h", "--help", "help":
		skillUsage()
		return exitOK
	default:
		errf("unknown subcommand: skill %s", sub)
		skillUsage()
		return exitUsage
	}
}

func skillUsage() {
	fmt.Fprintf(os.Stderr, `Usage:
  localapp skill show                       print the embedded SKILL.md
  localapp skill install <%[1]s>    install SKILL.md (idempotent)
  localapp skill uninstall <%[1]s>  remove the installed SKILL.md

Destination:
  default        ~/.claude/skills/localapp/SKILL.md (~/.codex/... for codex)
  --project      .claude/skills/localapp/SKILL.md relative to the cwd
  --dir <path>   <path>/localapp/SKILL.md (any skills directory)
`, strings.Join(skill.Targets(), "|"))
}

// cmdSkillShow prints the embedded SKILL.md to stdout.
func cmdSkillShow(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: localapp skill show")
		return exitUsage
	}
	if _, err := os.Stdout.WriteString(localapp.SkillMD); err != nil {
		return reportError(err)
	}
	return exitOK
}

func cmdSkillInstall(args []string) int {
	target, sc, done, code := parseSkillArgs("install", args)
	if done {
		return code
	}
	path, err := skill.Install(localapp.SkillMD, target, sc)
	if err != nil {
		return reportError(err)
	}
	fmt.Println(path)
	errf("installed. The agent may need to be restarted")
	return exitOK
}

func cmdSkillUninstall(args []string) int {
	target, sc, done, code := parseSkillArgs("uninstall", args)
	if done {
		return code
	}
	path, removed, err := skill.Uninstall(target, sc)
	if err != nil {
		return reportError(err)
	}
	if !removed {
		errf("not installed: %s", path)
		return exitOK
	}
	fmt.Println(path)
	errf("removed")
	return exitOK
}

// parseSkillArgs parses the flags and positional arguments shared by install
// and uninstall. With --dir the target name may be omitted.
// When done is true the caller returns code as is (help output or a usage
// error).
func parseSkillArgs(name string, args []string) (target string, sc skill.Scope, done bool, code int) {
	fs := newFlagSet("skill " + name)
	project := fs.Bool("project", false, "install into .claude / .codex relative to the cwd")
	dir := fs.String("dir", "", "skills directory to install into (<path>/localapp/SKILL.md)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: localapp skill %s <%s> [--project] [--dir <path>]\n",
			name, strings.Join(skill.Targets(), "|"))
	}
	pos, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", skill.Scope{}, true, exitOK
		}
		return "", skill.Scope{}, true, exitUsage
	}
	if *project && *dir != "" {
		errf("--project and --dir cannot be combined")
		return "", skill.Scope{}, true, exitUsage
	}
	switch len(pos) {
	case 0:
		// With --dir the target name is not needed.
		if *dir == "" {
			fs.Usage()
			return "", skill.Scope{}, true, exitUsage
		}
	case 1:
		target = pos[0]
	default:
		fs.Usage()
		return "", skill.Scope{}, true, exitUsage
	}
	return target, skill.Scope{Project: *project, Dir: *dir}, false, exitOK
}
