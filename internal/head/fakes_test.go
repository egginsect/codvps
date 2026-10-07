package head

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/egginsect/codvps/internal/claudeconfig"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/runner"
)

// fakeUnit is one claude-remote@ instance as the fake user manager sees it.
type fakeUnit struct {
	loaded   bool
	enabled  bool
	active   bool
	restarts int
}

// fakeSystemd is a small stateful model of `systemctl --user` for the
// claude-remote@ template, plus journalctl and the version/loginctl probes
// status uses. Like the real Claude CLI (and the fake-VPS harness's fake
// claude), an instance only stays active if ~/.claude.json trusts its
// workspace; refuse forces a workspace to be refused regardless, modelling
// a Claude that rejects the trust codvps seeded.
type fakeSystemd struct {
	t        *testing.T
	home     string
	units    map[string]*fakeUnit
	refuse   map[string]bool
	fail     map[string]runner.Response // "verb unit" -> forced response
	calls    [][]string
	refusals []string
}

func newFakeSystemd(t *testing.T, home string) *fakeSystemd {
	return &fakeSystemd{t: t, home: home, units: map[string]*fakeUnit{}, refuse: map[string]bool{}, fail: map[string]runner.Response{}}
}

func (f *fakeSystemd) unit(name string) *fakeUnit {
	u, ok := f.units[name]
	if !ok {
		u = &fakeUnit{}
		f.units[name] = u
	}
	return u
}

// starts reports whether claude remote-control would stay up for unit.
func (f *fakeSystemd) starts(unit string) bool {
	name := strings.TrimSuffix(strings.TrimPrefix(unit, "claude-remote@"), ".service")
	workspace := filepath.Join(f.home, name)
	trusted, err := claudeconfig.WorkspaceTrusted(f.home, workspace)
	if err != nil {
		f.t.Fatalf("fake claude could not read trust: %v", err)
	}
	if !trusted || f.refuse[name] {
		f.refusals = append(f.refusals, workspace)
		return false
	}
	return true
}

func (f *fakeSystemd) Run(name string, args ...string) (string, string, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	switch name {
	case "systemctl":
		return f.systemctl(args)
	case "journalctl":
		return "-- journal for " + args[2] + " --\nWorkspace not trusted\n", "", 0, nil
	case "loginctl":
		return "yes\n", "", 0, nil
	case "node", "claude", "codex", "gh":
		return name + " 1.2.3\nextra line\n", "", 0, nil
	}
	return "", "unexpected command " + name, 127, fmt.Errorf("unexpected command %s", name)
}

func (f *fakeSystemd) RunWithIO(name string, args []string, _ io.Reader, _, _ io.Writer) (int, error) {
	_, _, code, err := f.Run(name, args...)
	return code, err
}

func (f *fakeSystemd) systemctl(args []string) (string, string, int, error) {
	if len(args) == 0 || args[0] != "--user" {
		// System scope: only the Codex unit, which these tests keep off.
		if len(args) > 0 && (args[0] == "is-enabled" || args[0] == "is-active") {
			return "", "", 3, fmt.Errorf("exit 3")
		}
		return "", "", 0, nil
	}
	args = args[1:]
	verb := args[0]
	unit := args[len(args)-1]
	for _, a := range args {
		if strings.HasPrefix(a, "claude-remote@") && !strings.Contains(a, "*") {
			unit = a
			break
		}
	}
	if resp, ok := f.fail[verb+" "+unit]; ok {
		return resp.Stdout, resp.Stderr, resp.ExitCode, resp.Err
	}
	switch verb {
	case "list-units":
		var b strings.Builder
		for _, n := range f.sortedUnits() {
			if u := f.units[n]; u.loaded || u.active {
				state := "inactive dead"
				if u.active {
					state = "active running"
				}
				fmt.Fprintf(&b, "%s loaded %s Claude Remote Control\n", n, state)
			}
		}
		return b.String(), "", 0, nil
	case "list-unit-files":
		var b strings.Builder
		b.WriteString("claude-remote@.service disabled enabled\n")
		for _, n := range f.sortedUnits() {
			if f.units[n].enabled {
				fmt.Fprintf(&b, "%s enabled enabled\n", n)
			}
		}
		return b.String(), "", 0, nil
	case "show":
		u, ok := f.units[args[1]]
		if !ok {
			u = &fakeUnit{}
		}
		active, sub, file := "inactive", "dead", "disabled"
		if u.active {
			active, sub = "active", "running"
		}
		if u.enabled {
			file = "enabled"
		}
		var b strings.Builder
		for i := 2; i+1 < len(args); i += 2 {
			switch args[i+1] {
			case "ActiveState":
				fmt.Fprintf(&b, "ActiveState=%s\n", active)
			case "SubState":
				fmt.Fprintf(&b, "SubState=%s\n", sub)
			case "UnitFileState":
				fmt.Fprintf(&b, "UnitFileState=%s\n", file)
			case "NRestarts":
				fmt.Fprintf(&b, "NRestarts=%d\n", u.restarts)
			}
		}
		return b.String(), "", 0, nil
	case "is-active":
		u := f.units[unit]
		if u != nil && u.active {
			if len(args) == 2 {
				return "active\n", "", 0, nil
			}
			return "", "", 0, nil
		}
		if len(args) == 2 {
			return "inactive\n", "", 3, fmt.Errorf("exit 3")
		}
		return "", "", 3, fmt.Errorf("exit 3")
	case "is-enabled":
		if u := f.units[unit]; u != nil && u.enabled {
			return "enabled\n", "", 0, nil
		}
		return "disabled\n", "", 1, fmt.Errorf("exit 1")
	case "reset-failed":
		if u := f.units[unit]; u == nil || !u.loaded {
			return "", "Failed to reset failed state of unit " + unit + ": Unit " + unit + " not loaded.", 1, fmt.Errorf("exit 1")
		}
		return "", "", 0, nil
	case "enable":
		u := f.unit(unit)
		u.loaded, u.enabled = true, true
		u.active = f.starts(unit)
		return "", "", 0, nil
	case "disable":
		u := f.unit(unit)
		u.enabled = false
		if len(args) == 3 && args[1] == "--now" {
			u.active = false
		}
		return "", "", 0, nil
	case "stop":
		f.unit(unit).active = false
		return "", "", 0, nil
	}
	return "", "unexpected systemctl verb " + verb, 1, fmt.Errorf("unexpected verb %s", verb)
}

func (f *fakeSystemd) sortedUnits() []string {
	names := make([]string, 0, len(f.units))
	for n := range f.units {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// callsMatching returns every recorded call whose argv starts with prefix.
func (f *fakeSystemd) callsMatching(prefix ...string) [][]string {
	var out [][]string
	for _, c := range f.calls {
		if len(c) >= len(prefix) && strings.Join(c[:len(prefix)], "\x00") == strings.Join(prefix, "\x00") {
			out = append(out, c)
		}
	}
	return out
}

// testHost is a temp HOME with a registry of fake primary checkouts.
type testHost struct {
	t      *testing.T
	layout *paths.Layout
	git    *runner.FakeRunner
	sd     *fakeSystemd
	diag   *bytes.Buffer
	claude *Claude
	sleeps int
}

func newTestHost(t *testing.T) *testHost {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, v := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "CODVPS_NAMESPACE"} {
		t.Setenv(v, "")
	}
	layout, err := paths.New("")
	if err != nil {
		t.Fatalf("paths.New: %v", err)
	}
	h := &testHost{t: t, layout: layout, git: runner.NewFakeRunner(), sd: newFakeSystemd(t, home), diag: &bytes.Buffer{}}
	h.claude, err = NewClaude(Options{
		Layout:  layout,
		Git:     h.git,
		Systemd: h.sd,
		Diag:    h.diag,
		Sleep:   func(time.Duration) { h.sleeps++ },
	})
	if err != nil {
		t.Fatalf("NewClaude: %v", err)
	}
	return h
}

// register creates primary checkouts for names and writes the registry.
func (h *testHost) register(names ...string) {
	h.t.Helper()
	existing, err := registry.Read(h.layout.RepositoriesPath())
	if err != nil {
		h.t.Fatalf("read registry: %v", err)
	}
	for _, name := range names {
		dest := filepath.Join(h.layout.Home(), name)
		if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o700); err != nil {
			h.t.Fatal(err)
		}
		h.git.SetResponse("git", []string{"-C", dest, "rev-parse", "--path-format=absolute", "--git-common-dir"},
			runner.Response{Stdout: filepath.Join(dest, ".git") + "\n"})
	}
	if err := registry.Write(h.layout.RepositoriesPath(), append(existing, names...)); err != nil {
		h.t.Fatalf("write registry: %v", err)
	}
}

func (h *testHost) login() {
	h.t.Helper()
	path := h.layout.ClaudeCredentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *testHost) trusted(name string) bool {
	h.t.Helper()
	ok, err := claudeconfig.WorkspaceTrusted(h.layout.Home(), filepath.Join(h.layout.Home(), name))
	if err != nil {
		h.t.Fatalf("WorkspaceTrusted: %v", err)
	}
	return ok
}

func (h *testHost) unitUp(name string) bool {
	u := h.sd.units["claude-remote@"+name+".service"]
	return u != nil && u.active && u.enabled
}

func (h *testHost) unitDown(name string) bool {
	u := h.sd.units["claude-remote@"+name+".service"]
	return u == nil || (!u.active && !u.enabled)
}

type fakeResponse = runner.Response

// failingTools is a runner on which every probe fails to execute.
type failingTools struct{}

func (failingTools) Run(name string, _ ...string) (string, string, int, error) {
	return "", "", 127, fmt.Errorf("exec: %q: executable file not found in $PATH", name)
}

func (failingTools) RunWithIO(name string, _ []string, _ io.Reader, _, _ io.Writer) (int, error) {
	return 127, fmt.Errorf("exec: %q: executable file not found in $PATH", name)
}
