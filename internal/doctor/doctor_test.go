package doctor

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/systemd"
)

const testOperator = "operator"

// fakeRunner answers every probe doctor makes from fields; it never runs
// a real binary and fails the test on anything unexpected.
type fakeRunner struct {
	t             *testing.T
	gitConfig     map[string]string
	ghAuthed      bool
	claudeRuns    bool
	linger        string
	bus           string
	unitFiles     string
	defaultWants  string
	userUnits     string
	userActive    map[string]string
	userEnabled   map[string]string
	dpkgOwnsAAppr bool
	journal       string
	// mainPID answers `systemctl --user show <unit> -p MainPID`.
	mainPID map[string]string
	// controlGroup answers `systemctl [--user] show <unit> -p ControlGroup`.
	controlGroup map[string]string
}

func (f *fakeRunner) Run(name string, args ...string) (string, string, int, error) {
	joined := strings.Join(args, " ")
	switch name {
	case "git":
		if len(args) == 4 && args[0] == "config" {
			if v, ok := f.gitConfig[args[3]]; ok {
				return v + "\n", "", 0, nil
			}
			return "", "", 1, nil
		}
		if len(args) == 5 && args[0] == "-C" && args[2] == "rev-parse" {
			return filepath.Join(args[1], ".git") + "\n", "", 0, nil
		}
	case "gh":
		if joined == "auth status" {
			if f.ghAuthed {
				return "", "", 0, nil
			}
			return "", "not logged in", 1, nil
		}
	case "claude":
		if joined == "--version" && f.claudeRuns {
			return "2.1.0 (Claude Code)\n", "", 0, nil
		}
		return "", "", 1, nil
	case "loginctl":
		return f.linger + "\n", "", 0, nil
	case "dpkg-query":
		if f.dpkgOwnsAAppr {
			return "apparmor: " + distroApparmorProfile + "\n", "", 0, nil
		}
		return "", "no path found", 1, nil
	case "journalctl":
		return f.journal, "", 0, nil
	case "systemctl":
		if n := len(args); n >= 4 && args[n-4] == "show" && args[n-2] == "-p" && args[n-1] == "ControlGroup" {
			return "ControlGroup=" + f.controlGroup[args[n-3]] + "\n", "", 0, nil
		}
		if n := len(args); n == 4 && args[0] == "show" && args[2] == "-p" && args[3] == "MainPID" {
			return "MainPID=" + f.mainPID[args[1]] + "\n", "", 0, nil
		}
		switch {
		case joined == "--user is-system-running":
			return f.bus + "\n", "", 0, nil
		case strings.HasPrefix(joined, "--user list-unit-files claude-remote@*.service"):
			return f.unitFiles, "", 0, nil
		case joined == "--user show default.target -p Wants --value":
			return f.defaultWants + "\n", "", 0, nil
		case strings.HasPrefix(joined, "--user list-units claude-remote@*.service"):
			return f.userUnits, "", 0, nil
		case len(args) == 5 && args[0] == "--user" && args[1] == "show" && args[3] == "-p" && args[4] == "MainPID":
			return "MainPID=" + f.mainPID[args[2]] + "\n", "", 0, nil
		case strings.HasPrefix(joined, "--user show claude-remote.slice"):
			return "MemoryHigh=4294967296\nMemoryMax=5368709120\nCPUQuotaPerSecUSec=3s\n", "", 0, nil
		case len(args) == 3 && args[0] == "--user" && args[1] == "is-active":
			return f.userActive[args[2]] + "\n", "", 3, nil
		case len(args) == 3 && args[0] == "--user" && args[1] == "is-enabled":
			if state, ok := f.userEnabled[args[2]]; ok {
				return state + "\n", "", 0, nil
			}
			return "disabled\n", "", 1, nil
		}
	}
	f.t.Fatalf("unexpected command: %s %v", name, args)
	return "", "", 1, nil
}

func (f *fakeRunner) RunWithIO(name string, args []string, _ io.Reader, _, _ io.Writer) (int, error) {
	f.t.Fatalf("unexpected interactive command: %s %v", name, args)
	return 1, nil
}

type fakeCodex struct {
	running bool
	reason  string
	enabled string
	active  string
}

func (f *fakeCodex) DaemonStatus() (bool, string, error) { return f.running, f.reason, nil }
func (f *fakeCodex) EnabledState() (string, error)       { return f.enabled, nil }
func (f *fakeCodex) UnitState() (string, string, error)  { return f.active, "exited", nil }

// host is one hermetic doctor run: a system root, an operator home, and
// the fakes.
type host struct {
	t      *testing.T
	root   string
	home   string
	run    *fakeRunner
	codex  *fakeCodex
	tools  map[string]bool
	env    map[string]string
	layout *paths.Layout
	// selected is the component selection doctor is given.
	selected map[string]bool
	// shellPath, when set, is the login-shell PATH capture doctor is given.
	shellPath func(base []string) (string, error)
	// proc is the fake /proc doctor reads process environments from.
	proc string
	// cgroup is the fake cgroup filesystem.
	cgroup string
	// versions answers `<binary> --version` by host path.
	versions map[string]string
}

// healthyHost is a fully working claude+codex host whose Codex daemon
// answers on its control socket.
func healthyHost(t *testing.T) *host {
	t.Helper()
	base := t.TempDir()
	h := &host{
		t:    t,
		root: filepath.Join(base, "root"),
		home: filepath.Join(base, "home", testOperator),
		run: &fakeRunner{
			t:          t,
			gitConfig:  map[string]string{"user.name": "Op", "user.email": "op@example.invalid", "url.git@github.com:.insteadOf": "https://github.com/"},
			ghAuthed:   true,
			claudeRuns: true,
			linger:     "yes",
			bus:        "running",
			unitFiles:  "claude-remote@hello.service enabled enabled\n",
			userUnits:  "claude-remote@hello.service loaded active running Claude Remote Control for hello\n",
			userActive: map[string]string{"claude-remote@hello.service": "active"},
		},
		codex: &fakeCodex{enabled: "enabled", active: "active"},
		tools: map[string]bool{"claude": true, "gh": true, "jq": true, "rg": true, "uv": true, "pnpm": true, "bwrap": true, "codex": true},
		env:   map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"},
	}
	h.codex.running = true
	h.versions = map[string]string{}
	layout, err := paths.NewForHome(h.home, "")
	if err != nil {
		t.Fatal(err)
	}
	h.layout = layout.WithSystemRoot(h.root)
	h.proc = filepath.Join(base, "proc")
	h.cgroup = filepath.Join(base, "cgroup")

	// System files.
	write(t, filepath.Join(h.root, nodePath), "", 0o755)
	write(t, filepath.Join(h.root, ApparmorProfile), "profile bwrap {}\n", 0o644)
	write(t, filepath.Join(h.root, systemd.CodvpsBinary), "", 0o755)
	write(t, filepath.Join(h.root, "usr/local/bin/codex"), "", 0o755)
	for _, u := range append(append(systemd.ClaudeUserUnits(), systemd.CodexSystemUnits()...), systemd.CodexWatchdogUserUnits()...) {
		write(t, filepath.Join(h.root, u.InstallPath), string(u.Content), 0o644)
	}
	h.selected = map[string]bool{"claude": true, "codex": true}

	// Operator state.
	mkdir(t, filepath.Join(h.home, ".config/codvps"), 0o700)
	write(t, filepath.Join(h.home, ".config/codvps/repositories"), "hello\n", 0o600)
	write(t, filepath.Join(h.home, ".config/codvps/claude-head-enabled"), "", 0o600)
	mkdir(t, filepath.Join(h.home, "hello/.git"), 0o755)
	mkdir(t, filepath.Join(h.home, "hello/.claude/worktrees/wt1"), 0o755)
	write(t, filepath.Join(h.home, ".claude/.credentials.json"), `{"secret":"do-not-print"}`, 0o600)
	write(t, filepath.Join(h.home, ".claude.json"), `{"remoteDialogSeen":true}`, 0o600)
	write(t, filepath.Join(h.home, ".ssh/id_ed25519"), "key", 0o600)
	write(t, filepath.Join(h.home, ".ssh/known_hosts"), "github.com ssh-ed25519 AAAA\n", 0o644)
	mkdir(t, filepath.Join(h.home, ".codex"), 0o700)
	write(t, filepath.Join(h.home, ".codex/auth.json"), `{"token":"do-not-print"}`, 0o600)
	write(t, filepath.Join(h.home, ".codex/packages/standalone/releases/fake/codex"), "", 0o755)
	if err := os.Symlink("releases/fake", filepath.Join(h.home, ".codex/packages/standalone/current")); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *host) doctor() (string, Report, error) {
	var out bytes.Buffer
	rep, err := Run(Options{
		Layout:   h.layout,
		Root:     h.root,
		Runner:   h.run,
		Operator: testOperator,
		LookupEnv: func(k string) (string, bool) {
			v, ok := h.env[k]
			return v, ok
		},
		LookPath: func(name string) (string, error) {
			if h.tools[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		ProcRoot:   h.proc,
		CgroupRoot: h.cgroup,
		ShellPath:  h.shellPath,
		Out:        &out,
		Components: h.components(),
	})
	return out.String(), rep, err
}

// components are the production providers' doctor components, as
// internal/providers builds them, with this host's selection and fakes.
func (h *host) components() []Component {
	return []Component{
		{Name: "claude", Selected: h.selected["claude"], Suite: ClaudeSuite("claude", "claude")},
		{Name: "codex", Selected: h.selected["codex"], Suite: CodexSuite("codex", "codex", h.codex)},
	}
}

func write(t *testing.T, path, content string, mode os.FileMode) {
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

func mkdir(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mustContain(t *testing.T, out string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(out, l) {
			t.Errorf("doctor output lacks %q:\n%s", l, out)
		}
	}
}

func mustNotContain(t *testing.T, out string, fragments ...string) {
	t.Helper()
	for _, f := range fragments {
		if strings.Contains(out, f) {
			t.Errorf("doctor output unexpectedly contains %q:\n%s", f, out)
		}
	}
}

// test_doctor_accepts_curated_codex_daemon and test_doctor_credentials
// (healthy half): a fully working host has no failures.
func TestDoctorHealthyHost(t *testing.T) {
	h := healthyHost(t)
	out, rep, err := h.doctor()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failures != 0 {
		t.Fatalf("healthy host reported %d failures:\n%s", rep.Failures, out)
	}
	mustContain(t, out,
		"PASS: node is present at /usr/bin/node",
		"PASS: claude --version runs",
		"PASS: claude-remote.slice is installed (MemoryHigh=4294967296 MemoryMax=5368709120 CPUQuotaPerSecUSec=3s)",
		"PASS: git identity is configured",
		"PASS: bubblewrap AppArmor profile is installed",
		"PASS: installed Codex unit runs the official daemon lifecycle",
		"PASS: codvps state root is a private operator-owned directory",
		"PASS: Codex worker binary is present: "+filepath.Join(h.home, ".codex/packages/standalone/releases/fake/codex"),
		"PASS: Codex Remote head is enabled and its daemon is reachable",
		"PASS: Linger=yes",
		"PASS: systemd user bus is reachable (running)",
		"PASS: Claude OAuth credential file is present",
		"PASS: Remote Control consent is seeded",
		"PASS: ANTHROPIC_API_KEY is unset",
		"PASS: installed unit clears Anthropic environment variables",
		"PASS: repository hello has an active Claude unit",
		"Doctor found no failures.",
	)
	mustNotContain(t, out, "FAIL:", "WARN:", "do-not-print")
}

// The embedded unit must itself satisfy the installed-unit check, so a
// template edit cannot silently make doctor fail every host.
func TestEmbeddedCodexUnitPassesTheUnitCheck(t *testing.T) {
	unit := systemd.CodexSystemUnits()[0].Content
	if !UnitHasLines(unit, CodexUnitRequiredLines) {
		t.Fatal("the embedded codex-remote@.service lacks a line doctor requires")
	}
	for _, line := range strings.Split(string(unit), "\n") {
		for _, marker := range retiredCodexUnitMarkers {
			if strings.HasPrefix(line, marker) {
				t.Fatalf("the embedded codex-remote@.service carries the retired sandbox directive %q", line)
			}
		}
	}
}

// A unit installed before the sandbox was removed still hides the control
// socket in a private /tmp: doctor fails it and names the fix.
func TestDoctorFailsTheRetiredSandboxedCodexUnit(t *testing.T) {
	h := healthyHost(t)
	unit := filepath.Join(h.root, "etc/systemd/system/codex-remote@.service")
	old := string(systemd.CodexSystemUnits()[0].Content) + "PrivateTmp=yes\nProtectHome=tmpfs\n"
	write(t, unit, old, 0o644)
	out, rep, _ := h.doctor()
	if rep.Failures == 0 {
		t.Fatal("doctor accepted the retired sandboxed Codex unit")
	}
	mustContain(t, out, "FAIL: installed Codex unit is missing or is not the current one; run sudo codvps install")
}

// The retired sandboxed unit left the control socket a symlink into its
// private /tmp; the host's codex CLI cannot follow it. Doctor warns and
// names the command that removes it, and stays quiet otherwise.
func TestDoctorWarnsAboutADanglingControlSocketLink(t *testing.T) {
	h := healthyHost(t)
	socket := filepath.Join(h.home, ".codex/app-server-control/app-server-control.sock")
	mkdir(t, filepath.Dir(socket), 0o700)
	if err := os.Symlink("/tmp/systemd-private-gone/tmp/app-server-control.sock", socket); err != nil {
		t.Fatal(err)
	}
	out, rep, _ := h.doctor()
	if rep.Failures != 0 {
		t.Fatalf("a dangling link is only a warning:\n%s", out)
	}
	mustContain(t, out, "WARN: "+socket+" is a dangling symlink left by the retired sandboxed Codex unit, so the codex CLI cannot reach the daemon; run codvps enable codex")

	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	out, _, _ = h.doctor()
	mustNotContain(t, out, "dangling symlink")
}

// test_doctor_detects_missing_identity.
func TestDoctorDetectsMissingIdentity(t *testing.T) {
	h := healthyHost(t)
	delete(h.run.gitConfig, "user.email")
	out, rep, err := h.doctor()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failures == 0 {
		t.Fatal("doctor accepted a missing git identity")
	}
	mustContain(t, out, "FAIL: git identity is incomplete; run: codvps login github", "Doctor found 1 failure(s).")
}

// test_doctor_credentials (missing half): with a Claude head enabled a
// missing credential is a failure that names the login command.
func TestDoctorClaudeCredentialMissing(t *testing.T) {
	h := healthyHost(t)
	if err := os.Remove(filepath.Join(h.home, ".claude/.credentials.json")); err != nil {
		t.Fatal(err)
	}
	out, rep, _ := h.doctor()
	if rep.Failures == 0 {
		t.Fatal("doctor accepted a missing Claude credential")
	}
	mustContain(t, out, "FAIL: Claude OAuth credential file is absent; run codvps login claude")
}

// With no Claude head at all, a missing credential is only a warning.
func TestDoctorClaudeCredentialMissingWithoutHead(t *testing.T) {
	h := healthyHost(t)
	for _, p := range []string{".claude/.credentials.json", ".config/codvps/claude-head-enabled"} {
		if err := os.Remove(filepath.Join(h.home, p)); err != nil {
			t.Fatal(err)
		}
	}
	h.run.unitFiles, h.run.userUnits = "", ""
	out, _, _ := h.doctor()
	mustContain(t, out, "WARN: Claude OAuth credential file is absent; run codvps login claude before enabling a Claude head")
	mustNotContain(t, out, "FAIL: Claude OAuth credential file is absent")
}

// test_doctor_rejects_invalid_claude_credential_source.
func TestDoctorRejectsInvalidClaudeCredentialSource(t *testing.T) {
	h := healthyHost(t)
	cred := filepath.Join(h.home, ".claude/.credentials.json")
	if err := os.Remove(cred); err != nil {
		t.Fatal(err)
	}
	mkdir(t, cred, 0o700)
	out, rep, _ := h.doctor()
	if rep.Failures == 0 {
		t.Fatal("doctor accepted a directory credential source")
	}
	mustContain(t, out, "FAIL: Claude OAuth credential source must be a real regular file when present; run codvps login claude")
}

// An enabled unit whose daemon is unreachable names the probe's reason.
func TestDoctorNamesWhyTheDaemonIsUnreachable(t *testing.T) {
	h := healthyHost(t)
	h.codex.running, h.codex.reason = false, "cannot connect to the app-server control socket"
	out, _, _ := h.doctor()
	mustContain(t, out,
		"FAIL: Codex Remote systemd unit is active but its daemon is unreachable (cannot connect to the app-server control socket); inspect: sudo journalctl -u codex-remote@operator.service")
}

// A daemon the operator started outside the enabled unit is only a warning.
func TestDoctorWarnsAboutADaemonOutsideTheUnit(t *testing.T) {
	h := healthyHost(t)
	h.codex.enabled, h.codex.active = "disabled", "inactive"
	out, rep, _ := h.doctor()
	if rep.Failures != 0 {
		t.Fatalf("a daemon outside the unit failed doctor:\n%s", out)
	}
	mustContain(t, out, "WARN: Codex Remote daemon is running outside the enabled codvps unit")
}

// test_deselected_component_is_refused_and_doctor_skips_it.
func TestDoctorSkipsDeselectedComponents(t *testing.T) {
	h := healthyHost(t)
	h.selected = map[string]bool{"claude": true}
	if err := os.RemoveAll(filepath.Join(h.home, ".codex")); err != nil {
		t.Fatal(err)
	}
	delete(h.tools, "codex")
	out, rep, _ := h.doctor()
	if rep.Failures != 0 {
		t.Fatalf("doctor failed on a deselected component:\n%s", out)
	}
	mustContain(t, out, "WARN: codex is not a selected component; skipping its checks")
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "FAIL:") && strings.Contains(strings.ToLower(line), "codex") {
			t.Fatalf("doctor failed a Codex check: %s", line)
		}
	}

	h.selected = map[string]bool{}
	out, _, _ = h.doctor()
	mustContain(t, out, "WARN: claude is not a selected component; skipping its checks")
	mustNotContain(t, out, "claude --version")
}

// test_repo_without_claude_unit_is_not_a_doctor_error: an enabled unit
// with no repository behind it is operator debris, not a failure.
func TestDoctorWarnsAboutAUnitWithoutRepository(t *testing.T) {
	h := healthyHost(t)
	h.run.unitFiles += "claude-remote@missing-dir.service enabled enabled\n"
	out, rep, _ := h.doctor()
	if rep.Failures != 0 {
		t.Fatalf("an orphaned unit failed doctor:\n%s", out)
	}
	mustContain(t, out, "WARN: enabled or running unit claude-remote@missing-dir.service has no repository at "+filepath.Join(h.home, "missing-dir"))
}

// A failed Claude unit is a failure, and its journal tail is shown.
func TestDoctorShowsTheJournalOfAFailedClaudeUnit(t *testing.T) {
	h := healthyHost(t)
	h.run.userActive["claude-remote@hello.service"] = "failed"
	h.run.journal = "claude[1]: Workspace not trusted\n"
	out, rep, _ := h.doctor()
	if rep.Failures != 1 {
		t.Fatalf("failures = %d:\n%s", rep.Failures, out)
	}
	mustContain(t, out, "FAIL: repository hello has a failed Claude unit\nclaude[1]: Workspace not trusted\n")
}

// test_bwrap_apparmor_profile: the codvps profile, or the distro's own
// when dpkg owns it.
func TestDoctorChecksTheBubblewrapProfile(t *testing.T) {
	h := healthyHost(t)
	if err := os.Remove(filepath.Join(h.root, ApparmorProfile)); err != nil {
		t.Fatal(err)
	}
	out, _, _ := h.doctor()
	mustContain(t, out, "FAIL: bubblewrap AppArmor profile is missing")

	write(t, filepath.Join(h.root, distroApparmorProfile), "profile bwrap {}\n", 0o644)
	out, _, _ = h.doctor()
	mustContain(t, out, "FAIL: bubblewrap AppArmor profile is missing")
	h.run.dpkgOwnsAAppr = true
	out, _, _ = h.doctor()
	mustContain(t, out, "PASS: bubblewrap AppArmor profile is installed")
}

// Secrets are reported by presence only.
func TestDoctorNeverPrintsSecretValues(t *testing.T) {
	h := healthyHost(t)
	h.env["ANTHROPIC_API_KEY"] = "sk-ant-secret-value"
	h.env["CLAUDE_CODE_OAUTH_TOKEN"] = "oauth-secret-value"
	h.env["ANTHROPIC_BASE_URL"] = "https://proxy.invalid/secret-path"
	out, rep, _ := h.doctor()
	if rep.Failures != 3 {
		t.Fatalf("failures = %d:\n%s", rep.Failures, out)
	}
	mustContain(t, out, "FAIL: ANTHROPIC_API_KEY must be unset", "FAIL: CLAUDE_CODE_OAUTH_TOKEN must be unset",
		"FAIL: ANTHROPIC_BASE_URL must be unset or exactly https://api.anthropic.com")
	mustNotContain(t, out, "secret")
}

// An unreachable user bus is explained when no login session exists.
func TestDoctorExplainsAnUnreachableUserBus(t *testing.T) {
	h := healthyHost(t)
	h.run.bus = ""
	delete(h.env, "XDG_RUNTIME_DIR")
	h.run.linger = "no"
	out, _, _ := h.doctor()
	mustContain(t, out,
		"FAIL: Linger is no; run loginctl enable-linger operator",
		"FAIL: systemd user bus is unreachable because XDG_RUNTIME_DIR is unset; run codvps from a login shell (ssh, or sudo -iu operator)")
}

// A state doctor cannot read (here an unreadable Claude head switch)
// means doctor cannot answer (exit 2), and nothing is printed.
func TestDoctorCannotRunOnUnreadableState(t *testing.T) {
	h := healthyHost(t)
	if err := os.Chmod(filepath.Join(h.home, ".config/codvps"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(h.home, ".config/codvps"), 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	out, _, err := h.doctor()
	if err == nil {
		t.Fatal("doctor ran on unreadable state")
	}
	if out != "" {
		t.Fatalf("doctor printed a partial report:\n%s", out)
	}
}

// A provider added to the component list needs no change here: its tools
// are required while it is selected, its install and health checks run
// in their phases, and it is reported as skipped when it is not selected.
func TestDoctorRunsAnAddedProvidersSuite(t *testing.T) {
	h := healthyHost(t)
	added := Component{Name: "fakecli", Selected: true, Suite: Suite{
		Tools:   []string{"fakecli"},
		Install: func(c *Check) { c.Pass("fakecli is installed") },
		Health:  func(c *Check) { c.Fail("fakecli service is down") },
	}}
	h.tools["fakecli"] = true
	var out bytes.Buffer
	opts := Options{
		Layout: h.layout, Root: h.root, Runner: h.run, Operator: testOperator,
		LookupEnv: func(k string) (string, bool) { v, ok := h.env[k]; return v, ok },
		LookPath: func(name string) (string, error) {
			if h.tools[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		Out:        &out,
		Components: append(h.components(), added),
	}
	rep, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out.String(), "PASS: fakecli is present", "PASS: fakecli is installed", "FAIL: fakecli service is down")
	if rep.Failures != 1 {
		t.Fatalf("failures = %d:\n%s", rep.Failures, out.String())
	}

	out.Reset()
	opts.Components[len(opts.Components)-1].Selected = false
	if _, err := Run(opts); err != nil {
		t.Fatal(err)
	}
	mustContain(t, out.String(), "WARN: fakecli is not a selected component; skipping its checks")
	mustNotContain(t, out.String(), "fakecli is present", "fakecli is installed", "fakecli service")
}

// A leftover separate Codex Remote home is named, since the head now runs
// from ~/.codex and would not see its login or threads.
func TestDoctorWarnsAboutALeftoverCodexRemoteHome(t *testing.T) {
	h := healthyHost(t)
	mkdir(t, filepath.Join(h.home, ".codex-remote"), 0o700)
	out, _, _ := h.doctor()
	mustContain(t, out, "WARN: "+filepath.Join(h.home, ".codex-remote")+" is no longer used: Codex Remote now runs from ~/.codex")
}
