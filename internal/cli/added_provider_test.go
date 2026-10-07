package cli

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egginsect/codvps/internal/components"
	"github.com/egginsect/codvps/internal/doctor"
	"github.com/egginsect/codvps/internal/head"
	"github.com/egginsect/codvps/internal/login"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/runner"
)

// fakeHead is a test-only head.
type fakeHead struct{ enabled *bool }

func (f fakeHead) SetEnabled(enable bool) error { *f.enabled = enable; return nil }
func (f fakeHead) ListRows() ([]head.ListRow, error) {
	return []head.ListRow{{Scope: "host", Target: "-", Enabled: "enabled", Active: "running"}}, nil
}
func (f fakeHead) StatusRow() (head.StatusRow, error) {
	return head.StatusRow{Unit: "active", SubState: "running", Daemon: "running"}, nil
}

// fakeRepoHead is the test-only head as the repo commands drive it; it
// records every call.
type fakeRepoHead struct{ calls *[]string }

func (f fakeRepoHead) record(call string) { *f.calls = append(*f.calls, call) }
func (f fakeRepoHead) Label() string      { return "Fakecli" }
func (f fakeRepoHead) Attach() error      { f.record("attach"); return nil }
func (f fakeRepoHead) Held() string       { return "" }
func (f fakeRepoHead) Column() string     { return "FAKECLI_HEAD" }
func (f fakeRepoHead) Revoke() (string, error) {
	f.record("revoke")
	return "", nil
}
func (f fakeRepoHead) Attached(name string) (bool, error) {
	f.record("attached " + name)
	return false, nil
}
func (f fakeRepoHead) Detach(name string) error { f.record("detach " + name); return nil }
func (f fakeRepoHead) Resync(name string) error { f.record("resync " + name); return nil }
func (f fakeRepoHead) Summary() (string, error) { return "fakecli=enabled", nil }
func (f fakeRepoHead) Cells(names []string) ([]string, error) {
	cells := make([]string, len(names))
	for i := range cells {
		cells[i] = "attached"
	}
	return cells, nil
}

// A coding CLI added to the provider set flows through the head, repo,
// login and doctor wiring with no command edit.
func TestAddedProviderFlowsThroughHeadsLoginAndDoctor(t *testing.T) {
	enabled := false
	var repoCalls []string
	fake := &providers.Provider{
		Name: "fakecli", Kind: providers.CodingCLI, Binary: "fakecli", Label: "Fakecli",
		Capabilities: providers.Capabilities{
			Install: providers.Yes, Configure: providers.No, Auth: providers.Yes, Update: providers.No,
			Remove: providers.No, Service: providers.No, NativeRemote: providers.Yes,
		},
		Credential: &providers.Credential{Path: ".fakecli/token", Stored: providers.NonEmptyFile},
		Login:      &providers.Login{Summary: "Authenticate fakecli", Command: login.CLI{Binary: "fakecli", Args: []string{"login"}}},
		Head: &providers.Head{
			Host: func(*head.Heads) head.Host { return fakeHead{&enabled} },
			Repo: func(*head.Heads, *paths.Layout, runner.Runner) repo.Head { return fakeRepoHead{&repoCalls} },
		},
		Doctor: func(*head.Heads) doctor.Suite {
			return doctor.Suite{Install: func(c *doctor.Check) { c.Pass("fakecli is fine") }}
		},
		Install: &providers.Install{},
	}
	set := append(providers.All(), fake)
	if err := set.Validate(); err != nil {
		t.Fatal(err)
	}
	layout := hermeticLayout(t)
	env := testHeadEnv(t, layout, runner.NewFakeRunner(), runner.NewFakeRunner())
	env.providers = set

	want := "fakecli is not a selected component; rerun with it selected, e.g.: sudo codvps install --components claude,codex,cursor,opencode,fakecli"
	if err := env.headSetEnabled("fakecli", true); err == nil || err.Error() != want {
		t.Fatalf("head enable fakecli unselected = %v", err)
	}
	if err := components.Write(components.RegistryPath(layout), &components.Registry{Version: components.RegistryVersion, CodingCLIs: []string{"fakecli"}, Switch: "none"}); err != nil {
		t.Fatal(err)
	}
	if err := env.headSetEnabled("fakecli", true); err != nil || !enabled {
		t.Fatalf("head enable fakecli = %v (enabled=%v)", err, enabled)
	}

	var list strings.Builder
	if err := head.RenderList(&list, set.HeadHosts(env.heads)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.String(), "fakecli    host") {
		t.Fatalf("head list lacks the added head:\n%s", list.String())
	}

	// repo add attaches the added head and repo remove revokes, detaches
	// and resyncs it, alongside the Claude and Codex heads.
	repoEnv, err := buildHeadEnv(headDeps{
		layout: layout, git: runner.NewExecRunner("GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"),
		system: runner.NewFakeRunner(), exec: func(...string) runner.Runner { return runner.NewFakeRunner() },
		providers: set, operator: "operator",
		sleep: func(time.Duration) {}, now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	src, remote := t.TempDir(), filepath.Join(t.TempDir(), "widget.git")
	for _, args := range [][]string{
		{"-C", src, "init", "-q"},
		{"-C", src, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "initial"},
		{"clone", "-q", "--bare", src, remote},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	manager := repoEnv.repoManager()
	if err := manager.Add(remote); err != nil {
		t.Fatalf("repo add: %v", err)
	}
	var table strings.Builder
	if err := manager.RenderList(&table); err != nil {
		t.Fatalf("repo list: %v", err)
	}
	if !strings.Contains(table.String(), " fakecli=enabled\n") || !strings.Contains(table.String(), "FAKECLI_HEAD") ||
		!strings.HasSuffix(table.String(), " attached\n") {
		t.Fatalf("repo list lacks the added head:\n%s", table.String())
	}
	if _, err := manager.Remove("widget"); err != nil {
		t.Fatalf("repo remove: %v", err)
	}
	if got := strings.Join(repoCalls, ", "); got != "attach, attached widget, revoke, detach widget, resync widget" {
		t.Fatalf("added head calls = %s", got)
	}

	fr := runner.NewFakeRunner()
	if err := runLogin(layout, set, "fakecli", &login.Context{Runner: fr, HomeDir: layout.Home()}); err != nil {
		t.Fatal(err)
	}
	if len(fr.Calls) != 1 || fr.Calls[0].Name != "fakecli" {
		t.Fatalf("login ran %+v", fr.Calls)
	}

	comps, err := doctorComponents(layout, set, env.heads)
	if err != nil {
		t.Fatal(err)
	}
	last := comps[len(comps)-1]
	if last.Name != "fakecli" || !last.Selected || last.Optional || last.Suite.Install == nil {
		t.Fatalf("doctor component = %+v", last)
	}
	for _, c := range comps {
		if c.Name == "claude" && c.Selected {
			t.Fatal("an unselected provider was given to doctor as selected")
		}
	}
}

// An unreadable component registry means doctor cannot answer.
func TestDoctorComponentsNeedAReadableRegistry(t *testing.T) {
	layout := hermeticLayout(t)
	if err := components.Write(components.RegistryPath(layout), &components.Registry{Version: components.RegistryVersion, CodingCLIs: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	env := testHeadEnv(t, layout, runner.NewFakeRunner(), runner.NewFakeRunner())
	if _, err := doctorComponents(layout, providers.All(), env.heads); err != nil {
		t.Fatal(err)
	}
	if err := components.Write(components.RegistryPath(layout), &components.Registry{Version: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := doctorComponents(layout, providers.All(), env.heads); err == nil || !strings.Contains(err.Error(), "corrupt component registry") {
		t.Fatalf("corrupt registry = %v", err)
	}
}

// TestRepoSelectedOnlyHeadNeedsSelection: a head marked RepoSelectedOnly is
// driven by repo add/list/remove only once its provider is selected, so
// adding such a provider leaves unselected hosts' repo commands unchanged.
func TestRepoSelectedOnlyHeadNeedsSelection(t *testing.T) {
	var calls []string
	fake := &providers.Provider{
		Name: "fakecli", Kind: providers.CodingCLI, Binary: "fakecli", Label: "Fakecli",
		Capabilities: providers.Capabilities{
			Install: providers.Yes, Configure: providers.No, Auth: providers.No, Update: providers.No,
			Remove: providers.No, Service: providers.No, NativeRemote: providers.Yes,
		},
		Head: &providers.Head{
			Repo:             func(*head.Heads, *paths.Layout, runner.Runner) repo.Head { return fakeRepoHead{&calls} },
			RepoSelectedOnly: true,
		},
		Install: &providers.Install{},
	}
	set := providers.Set{fake}
	layout := hermeticLayout(t)
	build := func() *headEnv {
		env, err := buildHeadEnv(headDeps{
			layout: layout, git: runner.NewFakeRunner(),
			system: runner.NewFakeRunner(), exec: func(...string) runner.Runner { return runner.NewFakeRunner() },
			providers: set, operator: "operator",
			sleep: func(time.Duration) {}, now: time.Now,
		})
		if err != nil {
			t.Fatal(err)
		}
		return env
	}

	var table strings.Builder
	if err := build().repoManager().RenderList(&table); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(table.String(), "FAKECLI_HEAD") {
		t.Fatalf("unselected RepoSelectedOnly head is listed:\n%s", table.String())
	}

	if err := components.Write(components.RegistryPath(layout), &components.Registry{Version: components.RegistryVersion, CodingCLIs: []string{"fakecli"}, Switch: "none"}); err != nil {
		t.Fatal(err)
	}
	table.Reset()
	if err := build().repoManager().RenderList(&table); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table.String(), "FAKECLI_HEAD") {
		t.Fatalf("selected RepoSelectedOnly head is missing:\n%s", table.String())
	}
}
