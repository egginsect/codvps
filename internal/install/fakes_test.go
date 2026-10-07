package install

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/components"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/providers"
)

// fakeUnit is one unit as a fake manager sees it.
type fakeUnit struct {
	enabled bool
	active  bool
}

// fakeHost models loginctl and the system and operator --user managers
// that install and uninstall drive, recording every call. Nothing here
// ever executes a real binary.
type fakeHost struct {
	t      *testing.T
	system map[string]*fakeUnit
	user   map[string]*fakeUnit
	calls  []string
	// failUser makes the operator's manager reject every call, as it does
	// when the user bus is unreachable.
	failUser bool
	// versions answers `runuser ... <binary> --version` (install's seed
	// probe, run as the operator) by binary path.
	versions map[string]string
}

// testCatalog reads the component registry the way install does.
var testCatalog = components.Catalog{Providers: providers.All()}

func newFakeHost(t *testing.T) *fakeHost {
	return &fakeHost{t: t, system: map[string]*fakeUnit{}, user: map[string]*fakeUnit{}, versions: map[string]string{}}
}

func (f *fakeHost) unit(scope map[string]*fakeUnit, name string) *fakeUnit {
	u, ok := scope[name]
	if !ok {
		u = &fakeUnit{}
		scope[name] = u
	}
	return u
}

func (f *fakeHost) Run(name string, args ...string) (string, string, int, error) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	switch name {
	case "loginctl":
		return "", "", 0, nil
	case "systemctl":
		return f.systemctl(f.system, args)
	case "runuser":
		// runuser -u <op> -- env HOME=... DISABLE_*=1 <binary> --version
		if len(args) == 9 && args[3] == "env" && strings.HasPrefix(args[4], "HOME=") && args[8] == "--version" {
			if v, ok := f.versions[args[7]]; ok {
				return v + "\n", "", 0, nil
			}
			return "", "not executable", 126, nil
		}
		// runuser -u <op> -- env XDG_RUNTIME_DIR=... systemctl --user <args>
		if len(args) < 7 || args[0] != "-u" || args[2] != "--" || args[3] != "env" || args[5] != "systemctl" || args[6] != "--user" {
			f.t.Fatalf("unexpected runuser call: %v", args)
		}
		if f.failUser {
			return "", "Failed to connect to bus", 1, nil
		}
		return f.systemctl(f.user, args[7:])
	}
	f.t.Fatalf("unexpected command: %s %v", name, args)
	return "", "", 1, nil
}

func (f *fakeHost) RunWithIO(name string, args []string, _ io.Reader, _, _ io.Writer) (int, error) {
	f.t.Fatalf("unexpected interactive command: %s %v", name, args)
	return 1, nil
}

func (f *fakeHost) systemctl(scope map[string]*fakeUnit, args []string) (string, string, int, error) {
	verb := args[0]
	rest := args[1:]
	unitArg := func() string {
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") {
				return a
			}
		}
		return ""
	}
	switch verb {
	case "daemon-reload", "start":
		return "", "", 0, nil
	case "is-enabled":
		if u, ok := scope[unitArg()]; ok && u.enabled {
			return "enabled\n", "", 0, nil
		}
		return "disabled\n", "", 1, nil
	case "is-active":
		if u, ok := scope[unitArg()]; ok && u.active {
			return "active\n", "", 0, nil
		}
		return "inactive\n", "", 3, nil
	case "stop":
		f.unit(scope, unitArg()).active = false
		return "", "", 0, nil
	case "enable":
		for _, name := range rest {
			if strings.HasPrefix(name, "-") {
				continue
			}
			u := f.unit(scope, name)
			u.enabled = true
			if contains(rest, "--now") {
				u.active = true
			}
		}
		return "", "", 0, nil
	case "disable":
		u, ok := scope[unitArg()]
		if !ok {
			return "", "Unit file does not exist.", 1, nil
		}
		u.enabled = false
		if contains(rest, "--now") {
			u.active = false
		}
		return "", "", 0, nil
	case "list-units", "list-unit-files":
		pattern := unitArg()
		var names []string
		for name, u := range scope {
			if ok, _ := filepath.Match(pattern, name); ok && (u.enabled || u.active) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		var b strings.Builder
		for _, n := range names {
			fmt.Fprintf(&b, "%s enabled\n", n)
		}
		return b.String(), "", 0, nil
	}
	f.t.Fatalf("unexpected systemctl verb: %v", args)
	return "", "", 1, nil
}

func (f *fakeHost) called(want string) bool {
	for _, c := range f.calls {
		if c == want {
			return true
		}
	}
	return false
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// fakeIdent is the operator identity: the test process's own uid/gid, so
// ownership checks pass without root.
type fakeIdent struct{ home string }

func (f fakeIdent) OperatorHome(string) (string, error) { return f.home, nil }
func (f fakeIdent) OperatorIDs(string) (int, int, error) {
	return os.Getuid(), os.Getgid(), nil
}
func (f fakeIdent) EUID() int { return 0 }

// fakeGit answers the one git query IsPrimaryGitCheckout makes.
type fakeGit struct{ t *testing.T }

func (g fakeGit) Run(name string, args ...string) (string, string, int, error) {
	if name != "git" || len(args) != 5 || args[0] != "-C" || args[2] != "rev-parse" {
		g.t.Fatalf("unexpected git call: %s %v", name, args)
	}
	return filepath.Join(args[1], ".git") + "\n", "", 0, nil
}

func (g fakeGit) RunWithIO(name string, args []string, _ io.Reader, _, _ io.Writer) (int, error) {
	g.t.Fatalf("unexpected interactive git call: %v", args)
	return 1, nil
}

// env is one hermetic install host: a system root, an operator home, the
// fake managers, and captured output.
type env struct {
	t             *testing.T
	root          string
	home          string
	binary        string
	host          *fakeHost
	out           bytes.Buffer
	diag          bytes.Buffer
	opts          Options
	operatorCalls []string
}

const testOperator = "operator"

func newEnv(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	e := &env{t: t, root: filepath.Join(base, "root"), home: filepath.Join(base, "home", testOperator)}
	for _, d := range []string{e.root, e.home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.binary = filepath.Join(base, "build", "codvps")
	writeTestFile(t, e.binary, "codvps-binary-v1", 0o755)
	e.host = newFakeHost(t)
	e.opts = Options{
		Root:     e.root,
		EUID:     0,
		SudoUser: testOperator,
		Ident:    fakeIdent{home: e.home},
		Runner:   e.host,
		OperatorState: func(op Operator, namespace string, names []string) error {
			layout, err := paths.NewForHome(op.Home, namespace)
			if err != nil {
				return err
			}
			var selected providers.Set
			for _, name := range names {
				selected = append(selected, providers.All().Lookup(name))
			}
			return EnsureOperatorState(layout, fakeGit{t: t}, selected)
		},
		AsOperator: func(op Operator, namespace string, args ...string) error {
			return e.asOperator(op, namespace, args...)
		},
		ProcRoot:   t.TempDir(), // no running process: never the host's /proc
		Executable: func() (string, error) { return e.binary, nil },
		Out:        &e.out,
		Diag:       &e.diag,
		Providers:  providers.All(),
	}
	return e
}

// asOperator stands in for `codvps internal <args...>` run as the
// operator; install runs none since the pinned runtimes were retired.
func (e *env) asOperator(op Operator, namespace string, args ...string) error {
	e.operatorCalls = append(e.operatorCalls, strings.Join(args, " "))
	e.t.Fatalf("unexpected operator command: %v", args)
	return nil
}

func (e *env) install(args ...string) error {
	e.t.Helper()
	e.out.Reset()
	e.diag.Reset()
	return Install(e.opts, args)
}

func (e *env) uninstall(args ...string) error {
	e.t.Helper()
	e.out.Reset()
	e.diag.Reset()
	return Uninstall(e.opts, args)
}

func (e *env) sys(p string) string { return filepath.Join(e.root, p) }

// tool puts an executable at ~/.local/bin/<name>.
func (e *env) tool(name string) {
	writeTestFile(e.t, filepath.Join(e.home, ".local", "bin", name), "#!/bin/sh\n", 0o755)
}

// checkout creates ~/<name> as a primary Git checkout.
func (e *env) checkout(name string) {
	if err := os.MkdirAll(filepath.Join(e.home, name, ".git"), 0o755); err != nil {
		e.t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != want {
		t.Fatalf("%s mode = %o, want %o", path, fi.Mode().Perm(), want)
	}
}

func mustAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s should be absent (err=%v)", path, err)
	}
}

// tree lists every path under dir with its type and mode, so a test can
// prove a refused or dry run changed nothing.
func tree(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(&b, "%s %s\n", rel, fi.Mode())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
