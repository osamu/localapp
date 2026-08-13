package main

import (
	"flag"
	"io"
	"os"
	"reflect"
	"testing"
)

// Flags must be accepted after positional arguments, as in the DESIGN.md
// examples (localapp add 8000 --path /api).
func TestParseArgsAllowsFlagsAfterPositional(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantPos  []string
		wantApp  string
		wantPath string
		wantJSON bool
	}{
		{"no flags", []string{"5173"}, []string{"5173"}, "", "", false},
		{"flag after the positional argument", []string{"8000", "--path", "/api"}, []string{"8000"}, "", "/api", false},
		{"flag before the positional argument", []string{"--path", "/api", "8000"}, []string{"8000"}, "", "/api", false},
		{"= notation", []string{"8000", "--path=/api", "--app=app2"}, []string{"8000"}, "app2", "/api", false},
		{"single hyphen", []string{"3000", "-app", "app2", "-json"}, []string{"3000"}, "app2", "", true},
		{"positional argument between flags", []string{"--app", "app2", "3000", "--json"}, []string{"3000"}, "app2", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("add", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			app := fs.String("app", "", "")
			path := fs.String("path", "", "")
			asJSON := fs.Bool("json", false, "")

			pos, err := parseArgs(fs, tt.args)
			if err != nil {
				t.Fatalf("parseArgs: %v", err)
			}
			if !reflect.DeepEqual(pos, tt.wantPos) {
				t.Errorf("positional arguments = %v, want %v", pos, tt.wantPos)
			}
			if *app != tt.wantApp || *path != tt.wantPath || *asJSON != tt.wantJSON {
				t.Errorf("flags = app:%q path:%q json:%v, want app:%q path:%q json:%v",
					*app, *path, *asJSON, tt.wantApp, tt.wantPath, tt.wantJSON)
			}
		})
	}
}

func TestParseArgsRejectsUnknownFlag(t *testing.T) {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if _, err := parseArgs(fs, []string{"5173", "--nope"}); err == nil {
		t.Error("an unknown flag did not produce an error")
	}
}

// The default app name is derived deterministically from the basename of the
// cwd.
func TestDefaultAppName(t *testing.T) {
	dir := t.TempDir() + "/My_App"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	got, err := defaultAppName()
	if err != nil {
		t.Fatal(err)
	}
	if got != "my-app" {
		t.Errorf("defaultAppName() = %q, want %q", got, "my-app")
	}
}

func TestRunUnknownCommandIsUsageError(t *testing.T) {
	if code := run([]string{"nosuchcommand"}); code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if code := run(nil); code != exitUsage {
		t.Errorf("exit code with no arguments = %d, want %d", code, exitUsage)
	}
	if code := run([]string{"help"}); code != exitOK {
		t.Errorf("exit code of help = %d, want %d", code, exitOK)
	}
}

func TestRunBadUsage(t *testing.T) {
	tests := [][]string{
		{"add"},                  // missing port
		{"add", "not-a-number"},  // port is not a number
		{"add", "1", "2"},        // too many positional arguments
		{"rm"},                   // missing argument
		{"rm", "app1/web/extra"}, // invalid path notation
		{"ls", "extra"},
		{"status", "extra"},
	}
	for _, args := range tests {
		if code := run(args); code != exitUsage {
			t.Errorf("run(%v) = %d, want %d", args, code, exitUsage)
		}
	}
}
