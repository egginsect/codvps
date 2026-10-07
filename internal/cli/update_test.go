package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/paths"
)

// updateRunner answers --version from a table the fake updater bumps, and
// reports every claude head active.
type updateRunner struct {
	version map[string]string
	calls   []string
}

func (r *updateRunner) Run(name string, args ...string) (string, string, int, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, call)
	switch {
	case len(args) == 1 && args[0] == "--version":
		return r.version[name] + "\n", "", 0, nil
	case strings.Contains(call, "list-units claude-remote@*.service"):
		return "claude-remote@hello.service loaded active running\n", "", 0, nil
	case strings.Contains(call, "is-active"):
		return "active\n", "", 0, nil
	}
	return "", "", 0, nil
}

func (r *updateRunner) RunWithIO(string, []string, io.Reader, io.Writer, io.Writer) (int, error) {
	return 0, nil
}

type bumpingProvisioner struct {
	home string
	r    *updateRunner
	bin  string
}

func (b *bumpingProvisioner) Home() string                { return b.home }
func (b *bumpingProvisioner) Cmd(string, ...string) error { return nil }
func (b *bumpingProvisioner) AsOperator([]string, string) error {
	b.r.version[b.bin] = "2.0.0 (Claude Code)"
	return nil
}

func updateFixture(t *testing.T) (*paths.Layout, *updateRunner, *bumpingProvisioner) {
	home := t.TempDir()
	layout, err := paths.NewForHome(home, "")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &updateRunner{version: map[string]string{bin: "1.0.0 (Claude Code)"}}
	return layout, r, &bumpingProvisioner{home: home, r: r, bin: bin}
}

// Without a terminal and without --yes, update never prompts and never
// restarts; it names the units and the flag.
func TestUpdateWithoutTTYNamesTheRestart(t *testing.T) {
	layout, r, prov := updateFixture(t)
	var out strings.Builder
	if err := update(&out, strings.NewReader(""), layout, r, prov, "operator", []string{"claude", "codex"}, false, false); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"claude: 1.0.0 (Claude Code) -> 2.0.0 (Claude Code)", "codex: not installed (codvps enable codex installs it)",
		"Rerun with --yes to restart onto the new versions (interrupts live sessions): claude-remote@hello.service"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	for _, c := range r.calls {
		if strings.Contains(c, "restart claude-remote") {
			t.Fatalf("restarted without --yes: %v", r.calls)
		}
	}
}

// --yes restarts the active heads of a CLI that changed; an unchanged
// CLI restarts nothing.
func TestUpdateWithYesRestartsChangedHeads(t *testing.T) {
	layout, r, prov := updateFixture(t)
	var out strings.Builder
	if err := update(&out, nil, layout, r, prov, "operator", []string{"claude"}, true, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "restarted claude-remote@hello.service") {
		t.Fatalf("output:\n%s", out.String())
	}
	out.Reset()
	r.calls = nil
	if err := update(&out, nil, layout, r, prov, "operator", []string{"claude"}, true, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "claude: 2.0.0 (Claude Code) is current") || strings.Contains(out.String(), "restarted") {
		t.Fatalf("unchanged CLI output:\n%s", out.String())
	}
}
