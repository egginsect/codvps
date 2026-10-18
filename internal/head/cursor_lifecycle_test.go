package head

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egginsect/codvps/internal/paths"
)

func TestCursorInstancesIncludesLoadedAndEnabledOnce(t *testing.T) {
	c := &Cursor{r: &fakeRunner{runFunc: func(_ string, args ...string) (string, string, int, error) {
		switch args[1] {
		case "list-units":
			return "cursor-remote@running.service loaded active running Worker\ncursor-remote@both.service loaded inactive dead Worker\n", "", 0, nil
		case "list-unit-files":
			return "cursor-remote@.service disabled\ncursor-remote@both.service enabled\ncursor-remote@enabled.service enabled\nother.service enabled\ncursor-remote@invalid.timer enabled\n", "", 0, nil
		default:
			t.Fatalf("unexpected systemctl arguments: %v", args)
			return "", "", 1, nil
		}
	}}}
	got, err := c.instances(true)
	if err != nil {
		t.Fatal(err)
	}
	want := []cursorInstance{
		{unit: "cursor-remote@running.service", name: "running"},
		{unit: "cursor-remote@both.service", name: "both"},
		{unit: "cursor-remote@enabled.service", name: "enabled"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("instances = %#v, want %#v", got, want)
	}
}

func TestCursorInstancesPropagatesCensusErrors(t *testing.T) {
	for _, test := range []struct {
		name, query, stderr string
		code                int
		err                 error
	}{
		{name: "missing bus", query: "list-units", stderr: "Failed to connect to bus", code: 1},
		{name: "empty failed loaded census", query: "list-units", code: 1},
		{name: "missing executable", query: "list-unit-files", err: errors.New("not found")},
		{name: "unit files bus error", query: "list-unit-files", stderr: "Failed to connect to bus", code: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := &Cursor{r: &fakeRunner{runFunc: func(_ string, args ...string) (string, string, int, error) {
				if args[1] == test.query {
					return "", test.stderr, test.code, test.err
				}
				return "", "", 0, nil
			}}}
			if _, err := c.instances(true); err == nil {
				t.Fatal("census failure was reported as success")
			}
		})
	}
}

func TestCursorDisableStopsLoadedWorkersWithoutRegistry(t *testing.T) {
	layout, err := paths.NewForHome(t.TempDir(), "codvps")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.CursorHeadFlagPath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.RepositoriesPath(), []byte("{corrupt registry"), 0o600); err != nil {
		t.Fatal(err)
	}
	noMatchErr := exec.Command("sh", "-c", "exit 1").Run()
	var exitErr *exec.ExitError
	if !errors.As(noMatchErr, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("prepare real no-match exit status: %v", noMatchErr)
	}
	var stopped []string
	c := &Cursor{
		layout: layout, diag: io.Discard, sleep: func(time.Duration) {},
		git: &fakeRunner{runFunc: func(string, ...string) (string, string, int, error) {
			t.Fatal("disable must not inspect checkouts")
			return "", "", 1, nil
		}},
		r: &fakeRunner{runFunc: func(_ string, args ...string) (string, string, int, error) {
			switch args[1] {
			case "list-units":
				return "cursor-remote@external.service loaded active running Worker\ncursor-remote@stale.service loaded active running Worker\n", "", 0, nil
			case "list-unit-files":
				return "", "", 1, noMatchErr // Real ExecRunner exit semantics, no instantiated unit files.
			case "disable":
				if args[2] != "--now" {
					t.Fatalf("disable did not request stop: %v", args)
				}
				stopped = append(stopped, args[3])
				return "", "", 0, nil
			default:
				t.Fatalf("unexpected command: %v", args)
				return "", "", 1, nil
			}
		}},
	}
	if err := c.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(layout.CursorHeadFlagPath()); !os.IsNotExist(err) {
		t.Fatalf("enabled flag still exists: %v", err)
	}
	if want := []string{"cursor-remote@external.service", "cursor-remote@stale.service"}; !reflect.DeepEqual(stopped, want) {
		t.Fatalf("stopped = %v, want %v", stopped, want)
	}
	data, err := os.ReadFile(layout.RepositoriesPath())
	if err != nil || string(data) != "{corrupt registry" {
		t.Fatalf("disable changed registry: %q, %v", data, err)
	}
}

func TestCursorPruningAdapterStopsCursorWorker(t *testing.T) {
	var calls []string
	c := &Cursor{r: &fakeRunner{runFunc: func(name string, args ...string) (string, string, int, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", "", 0, nil
	}}}
	if err := c.DisableClaudeUnit("vanished"); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl --user disable --now cursor-remote@vanished.service"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("prune commands = %v, want %v", calls, want)
	}
}
