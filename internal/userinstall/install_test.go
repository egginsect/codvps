package userinstall

import (
	"bytes"
	"fmt"
	"github.com/egginsect/codvps/internal/runner"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (Options, *runner.FakeRunner, *bytes.Buffer) {
	t.Helper()
	r := runner.NewFakeRunner()
	out := new(bytes.Buffer)
	return Options{Home: t.TempDir(), EUID: 1000, Runner: r, Out: out, Diag: out}, r, out
}
func TestDryRunHasNoWritesOrCommands(t *testing.T) {
	o, r, out := fixture(t)
	if err := Install(o, []string{"--dry-run"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(o.Home)
	if len(entries) != 0 || len(r.Calls) != 0 {
		t.Fatal("dry-run mutated or executed")
	}
	for _, p := range providers {
		if !strings.Contains(out.String(), p.url) {
			t.Fatal("missing official source", p.name)
		}
	}
}
func TestInvalidFlagsFailBeforeCommands(t *testing.T) {
	for _, args := range [][]string{{"--skip-provision"}, {"--switch", "none"}, {"--components", "bad"}, {"--components", "claude,claude"}, {"--components"}, {"--components", "claude", "--components", "cursor"}} {
		o, r, _ := fixture(t)
		if err := Install(o, args); err == nil {
			t.Fatal("accepted", args)
		}
		if len(r.Calls) > 0 {
			t.Fatal("executed", args)
		}
	}
}
func TestRejectRoot(t *testing.T) {
	o, r, _ := fixture(t)
	o.EUID = 0
	if Install(o, nil) == nil {
		t.Fatal("accepted root")
	}
	if len(r.Calls) > 0 {
		t.Fatal("executed")
	}
}
func TestKeepExistingUserExecutables(t *testing.T) {
	o, r, out := fixture(t)
	for _, p := range providers {
		bin := filepath.Join(o.Home, p.binary)
		if err := os.MkdirAll(filepath.Dir(bin), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, []byte("preserved"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := Install(o, nil); err != nil {
		t.Fatal(err)
	}
	if len(r.Calls) != 3 {
		t.Fatal(r.Calls)
	}
	for _, c := range r.Calls {
		if len(c.Args) != 1 || c.Args[0] != "--version" {
			t.Fatal(c)
		}
	}
	if strings.Count(out.String(), "kept unchanged") != 3 {
		t.Fatal(out.String())
	}
}
func TestRejectEscapeBeforeAnyInstaller(t *testing.T) {
	o, r, _ := fixture(t)
	if err := os.MkdirAll(filepath.Join(o.Home, ".local"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(o.Home, ".local/bin")); err != nil {
		t.Fatal(err)
	}
	if Install(o, nil) == nil {
		t.Fatal("accepted outside home symlink")
	}
	if len(r.Calls) > 0 {
		t.Fatal("executed")
	}
}
func TestRejectBrokenExistingBinary(t *testing.T) {
	o, r, _ := fixture(t)
	bin := filepath.Join(o.Home, providers[0].binary)
	if err := os.MkdirAll(filepath.Dir(bin), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}
	if Install(o, []string{"--components", "claude"}) == nil {
		t.Fatal("accepted non executable")
	}
	if len(r.Calls) > 0 {
		t.Fatal("executed")
	}
}

type installingRunner struct {
	home  string
	calls []string
	fail  bool
}

func (r *installingRunner) Run(name string, args ...string) (string, string, int, error) {
	r.calls = append(r.calls, name)
	if name == "curl" && r.fail {
		return "", "download error", 1, fmt.Errorf("network")
	}
	return "version 1", "", 0, nil
}
func (r *installingRunner) RunWithIO(name string, args []string, in io.Reader, out, diag io.Writer) (int, error) {
	r.calls = append(r.calls, name)
	bin := filepath.Join(r.home, ".local/bin/claude")
	if err := os.MkdirAll(filepath.Dir(bin), 0755); err != nil {
		return 1, err
	}
	return 0, os.WriteFile(bin, []byte("native"), 0755)
}
func TestMissingUsesOfficialInstallerThenVerifies(t *testing.T) {
	o, _, _ := fixture(t)
	r := &installingRunner{home: o.Home}
	o.Runner = r
	if err := Install(o, []string{"--components", "claude"}); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 3 || r.calls[0] != "curl" || r.calls[1] != "bash" || r.calls[2] != filepath.Join(o.Home, ".local/bin/claude") {
		t.Fatal(r.calls)
	}
}
func TestFailedDownloadNeverExecutesInstaller(t *testing.T) {
	o, _, _ := fixture(t)
	r := &installingRunner{home: o.Home, fail: true}
	o.Runner = r
	if Install(o, []string{"--components", "claude"}) == nil {
		t.Fatal("accepted failure")
	}
	if len(r.calls) != 1 {
		t.Fatal(r.calls)
	}
}

func TestRejectEscapingProviderStorageBeforeExecution(t *testing.T) {
	for _, rel := range []string{".local/share/cursor-agent", ".local/share/claude", ".claude/downloads", ".codex/packages/standalone/releases"} {
		t.Run(rel, func(t *testing.T) {
			o, r, _ := fixture(t)
			path := filepath.Join(o.Home, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), path); err != nil {
				t.Fatal(err)
			}
			if Install(o, nil) == nil {
				t.Fatal("accepted escape")
			}
			if len(r.Calls) > 0 {
				t.Fatal("executed")
			}
		})
	}
}
func TestPreserveCursorAliasWithoutAgent(t *testing.T) {
	o, r, _ := fixture(t)
	alias := filepath.Join(o.Home, ".local/bin/cursor-agent")
	if err := os.MkdirAll(filepath.Dir(alias), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alias, []byte("keep"), 0755); err != nil {
		t.Fatal(err)
	}
	if Install(o, []string{"--components", "cursor"}) == nil {
		t.Fatal("accepted alias overwrite")
	}
	if len(r.Calls) > 0 {
		t.Fatal("executed")
	}
}

func TestRejectEscapingShellProfile(t *testing.T) {
	o, r, _ := fixture(t)
	target := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(o.Home, ".bashrc")); err != nil {
		t.Fatal(err)
	}
	if Install(o, nil) == nil {
		t.Fatal("accepted profile escape")
	}
	if len(r.Calls) > 0 {
		t.Fatal("executed")
	}
}

func TestRejectNestedStorageRedirect(t *testing.T) {
	o, r, _ := fixture(t)
	storage := filepath.Join(o.Home, "storage")
	if err := os.MkdirAll(storage, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(storage, "versions")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(o.Home, ".local/share"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(storage, filepath.Join(o.Home, ".local/share/cursor-agent")); err != nil {
		t.Fatal(err)
	}
	if Install(o, nil) == nil {
		t.Fatal("accepted nested redirect")
	}
	if len(r.Calls) > 0 {
		t.Fatal("executed")
	}
}
func TestAllowOfficialCodexCurrentSelection(t *testing.T) {
	o, r, _ := fixture(t)
	root := filepath.Join(o.Home, ".codex/packages/standalone")
	release := filepath.Join(root, "releases", "one")
	if err := os.MkdirAll(release, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, "codex"), []byte("native"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(release, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := Install(o, []string{"--components", "codex"}); err != nil {
		t.Fatal(err)
	}
	if len(r.Calls) != 1 {
		t.Fatal(r.Calls)
	}
}

func TestPreserveCodexWithoutStandalonePackage(t *testing.T) {
	o, r, _ := fixture(t)
	alias := filepath.Join(o.Home, ".local/bin/codex")
	if err := os.MkdirAll(filepath.Dir(alias), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alias, []byte("keep"), 0755); err != nil {
		t.Fatal(err)
	}
	if Install(o, []string{"--components", "codex"}) == nil {
		t.Fatal("accepted existing Codex overwrite")
	}
	if len(r.Calls) > 0 {
		t.Fatal("executed")
	}
}

type failingOutput struct{}

func (failingOutput) Write([]byte) (int, error) { return 0, fmt.Errorf("output unavailable") }
func TestOutputFailureStopsBeforeInstaller(t *testing.T) {
	o, r, _ := fixture(t)
	o.Out = failingOutput{}
	if err := Install(o, []string{"--components", "claude"}); err == nil {
		t.Fatal("ignored output error")
	}
	if len(r.Calls) != 0 {
		t.Fatal("ran installer after output failure")
	}
}
