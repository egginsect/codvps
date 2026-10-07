package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/components"
	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/systemd"
)

// test_install_requires_non_root_operator: bare root and SUDO_USER=root
// are refused with the reference's reason, before anything is written.
func TestInstallRequiresNonRootOperator(t *testing.T) {
	for _, sudoUser := range []string{"", "root"} {
		e := newEnv(t)
		e.opts.SudoUser = sudoUser
		err := e.install("--components", "claude")
		if err == nil || err.Error() != errNonRootOperator {
			t.Fatalf("SUDO_USER=%q: got %v", sudoUser, err)
		}
		if got := tree(t, e.root); got != ". drwxr-xr-x\n" {
			t.Fatalf("refused install wrote files:\n%s", got)
		}
	}
	e := newEnv(t)
	e.opts.EUID = 1000
	if err := e.install(); err == nil || !strings.Contains(err.Error(), "must be run as root") {
		t.Fatalf("non-root caller: got %v", err)
	}
}

// A fresh host with no flags, no registry and no terminal cannot guess.
func TestInstallFreshHostNeedsSelection(t *testing.T) {
	e := newEnv(t)
	err := e.install()
	if err == nil || err.Error() != testCatalog.FreshHostSelectionMessage() {
		t.Fatalf("got %v", err)
	}
	if got := tree(t, e.root); got != ". drwxr-xr-x\n" {
		t.Fatalf("refused install wrote files:\n%s", got)
	}
}

func TestInstallRejectsBadArguments(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{{"--bogus"}, {"--components"}, {"--switch"}, {"--components", "vim"}, {"--switch", "cc-switch"}} {
		if err := e.install(args...); err == nil {
			t.Errorf("install %v was accepted", args)
		}
	}
	if got := tree(t, e.root); got != ". drwxr-xr-x\n" {
		t.Fatalf("rejected install wrote files:\n%s", got)
	}
}

// Other namespaces would collide on the unprefixed unit names.
func TestInstallRefusesNonDefaultNamespace(t *testing.T) {
	e := newEnv(t)
	e.opts.Namespace = "test"
	if err := e.install("--components", "claude"); err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("got %v", err)
	}
}

// test_install_is_idempotent + test_new_tooling_on_path +
// test_linger_enabled: a full install writes the binary, every product
// unit, the registry, the operator's private state, linger and the tooling
// links; a second run changes nothing and duplicates nothing. It creates no
// Codex state: ~/.codex is Codex's own, and there is no membership manifest.
func TestInstallIsCompleteAndIdempotent(t *testing.T) {
	e := newEnv(t)
	for _, tool := range []string{"uv", "uvx", "codex"} {
		e.tool(tool)
	}
	for run := 1; run <= 2; run++ {
		if err := e.install("--components", "claude,codex", "--switch", "none"); err != nil {
			t.Fatalf("install run %d: %v\nstderr: %s", run, err, e.diag.String())
		}
	}

	reg, err := testCatalog.Read(e.sys("etc/codvps/components.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(reg.Selected(), ","); got != "claude,codex" || reg.SwitchValue() != "none" {
		t.Fatalf("registry = %v %s", reg.Selected(), reg.SwitchValue())
	}
	mustMode(t, e.sys("etc/codvps/components.json"), 0o644)
	mustMode(t, e.sys("etc/codvps"), 0o755)

	if got := readTestFile(t, e.sys(systemd.CodvpsBinary)); got != "codvps-binary-v1" {
		t.Fatalf("installed binary = %q", got)
	}
	mustMode(t, e.sys(systemd.CodvpsBinary), 0o755)
	for _, u := range ProductUnits(providers.All()) {
		if got := readTestFile(t, e.sys(u.InstallPath)); got != string(u.Content) {
			t.Fatalf("%s does not match the embedded template", u.InstallPath)
		}
		mustMode(t, e.sys(u.InstallPath), 0o644)
	}

	mustMode(t, filepath.Join(e.home, ".config/codvps"), 0o700)
	mustAbsent(t, filepath.Join(e.home, ".codex"))
	mustAbsent(t, filepath.Join(e.home, ".config/codvps/codex-repositories"))
	if n := strings.Count(readTestFile(t, filepath.Join(e.home, ".bashrc")), pathLine); n != 1 {
		t.Fatalf("~/.bashrc carries the PATH line %d times", n)
	}
	for _, tool := range []string{"uv", "uvx", "codex"} {
		target, err := os.Readlink(e.sys("usr/local/bin/" + tool))
		if err != nil || target != filepath.Join(e.home, ".local/bin", tool) {
			t.Fatalf("/usr/local/bin/%s -> %q (%v)", tool, target, err)
		}
	}

	for _, want := range []string{
		"loginctl enable-linger operator",
		"systemctl daemon-reload",
		"runuser -u operator -- env XDG_RUNTIME_DIR=/run/user/" + itoa(os.Getuid()) + " systemctl --user daemon-reload",
		"systemctl start user@" + itoa(os.Getuid()) + ".service",
	} {
		if !e.host.called(want) {
			t.Errorf("install never ran %q; calls:\n%s", want, strings.Join(e.host.calls, "\n"))
		}
	}

	if strings.Contains(e.out.String(), "replacing ") {
		t.Fatalf("a rerun reported replacing its own files:\n%s", e.out.String())
	}
	if !strings.Contains(e.out.String(), "codvps installation complete.") {
		t.Fatalf("missing completion message:\n%s", e.out.String())
	}
}

// A claude-only install writes no Codex state and needs no codex link.
func TestInstallClaudeOnlySkipsCodexState(t *testing.T) {
	e := newEnv(t)
	if err := e.install("--components", "claude"); err != nil {
		t.Fatal(err)
	}
	mustAbsent(t, filepath.Join(e.home, ".codex"))
	mustAbsent(t, filepath.Join(e.home, ".config/codvps/codex-repositories"))
	mustAbsent(t, e.sys("usr/local/bin/codex"))
	if !strings.Contains(e.diag.String(), "skipping system PATH symlink for /usr/local/bin/uv") {
		t.Fatalf("missing uv warning:\n%s", e.diag.String())
	}
}

// With codex selected but not installed yet (it is installed on first
// enable or login, as the operator), install still links the unit PATH
// entry to where the vendor install puts it.
func TestInstallLinksCodexBeforeItIsInstalled(t *testing.T) {
	e := newEnv(t)
	if err := e.install("--components", "codex"); err != nil {
		t.Fatalf("install: %v\n%s", err, e.diag.String())
	}
	if target, err := os.Readlink(e.sys("usr/local/bin/codex")); err != nil || target != filepath.Join(e.home, ".local/bin/codex") {
		t.Fatalf("/usr/local/bin/codex -> %q (%v)", target, err)
	}

	// A codex that already runs there is never replaced by a link to a
	// CLI that is not installed yet.
	e = newEnv(t)
	writeTestFile(t, e.sys("usr/local/bin/codex"), "#!/bin/sh\n", 0o755)
	if err := e.install("--components", "codex"); err != nil {
		t.Fatalf("install: %v\n%s", err, e.diag.String())
	}
	if fi, err := os.Lstat(e.sys("usr/local/bin/codex")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("a working /usr/local/bin/codex was replaced: %v %v", fi, err)
	}
}

// install is the single manager of its unit files: one that differs from
// the embedded template is replaced, and named before anything changes.
func TestInstallReplacesADifferentUnitFile(t *testing.T) {
	e := newEnv(t)
	existing := e.sys("etc/systemd/user/claude-remote@.service")
	writeTestFile(t, existing, "[Service]\nExecStart=/opt/other\n", 0o644)
	if err := e.install("--components", "claude"); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, existing); got != string(systemd.ClaudeUserUnits()[1].Content) {
		t.Fatalf("unit file was not replaced: %q", got)
	}
	out := e.out.String()
	replacing := strings.Index(out, "replacing /etc/systemd/user/claude-remote@.service: it differs from what codvps installs there")
	if replacing < 0 || replacing > strings.Index(out, "component selection:") {
		t.Fatalf("the replacement was not named before the install changed anything:\n%s", out)
	}
}

// A byte-identical unit file changes nothing when written, so it is not
// reported as replaced.
func TestInstallKeepsIdenticalUnitFileQuietly(t *testing.T) {
	e := newEnv(t)
	u := systemd.ClaudeUserUnits()[1]
	writeTestFile(t, e.sys(u.InstallPath), string(u.Content), 0o644)
	if err := e.install("--components", "claude"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.out.String(), "replacing") {
		t.Fatalf("an identical unit was reported as replaced:\n%s", e.out.String())
	}
}

// A running binary that already is /usr/local/bin/codvps is not copied
// onto itself, and any other binary there is replaced.
func TestInstallBinaryReplacement(t *testing.T) {
	e := newEnv(t)
	writeTestFile(t, e.sys(systemd.CodvpsBinary), "someone else's codvps", 0o755)
	if err := e.install("--components", "claude"); err != nil {
		t.Fatalf("existing binary: got %v", err)
	}
	if got := readTestFile(t, e.sys(systemd.CodvpsBinary)); got != "codvps-binary-v1" {
		t.Fatalf("binary = %q", got)
	}
	if !strings.Contains(e.out.String(), "replacing /usr/local/bin/codvps: it differs from what codvps installs there") {
		t.Fatalf("the binary replacement was not named:\n%s", e.out.String())
	}

	e = newEnv(t)
	e.binary = e.sys(systemd.CodvpsBinary)
	writeTestFile(t, e.binary, "in place", 0o755)
	e.opts.Executable = func() (string, error) { return e.binary, nil }
	if err := e.install("--components", "claude"); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, e.binary); got != "in place" {
		t.Fatalf("binary = %q", got)
	}
}

// A /usr/local/bin entry that is not codvps's link is replaced by it, and
// named.
func TestInstallReplacesAnExistingToolEntry(t *testing.T) {
	e := newEnv(t)
	e.tool("uv")
	existing := e.sys("usr/local/bin/uv")
	writeTestFile(t, existing, "#!/bin/sh\n# another uv\n", 0o755)
	if err := e.install("--components", "claude"); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(existing); err != nil || target != filepath.Join(e.home, ".local/bin/uv") {
		t.Fatalf("/usr/local/bin/uv -> %q (%v)", target, err)
	}
	if !strings.Contains(e.out.String(), "replacing /usr/local/bin/uv with the link to "+filepath.Join(e.home, ".local/bin/uv")) {
		t.Fatalf("the replacement was not named:\n%s", e.out.String())
	}
}

// An enabled legacy --user Codex head is migrated: the daemon is stopped,
// the legacy unit retired, and the system unit enabled and started.
func TestInstallMigratesEnabledCodexHeadToTheSystemUnit(t *testing.T) {
	e := newEnv(t)
	e.tool("codex")
	writeTestFile(t, filepath.Join(e.home, ".codex/auth.json"), `{"token":"x"}`, 0o600)
	legacyUnit := e.sys("etc/systemd/user/codex-remote.service")
	writeTestFile(t, legacyUnit, "[Service]\n", 0o644)
	e.host.user["codex-remote.service"] = &fakeUnit{enabled: true, active: true}

	if err := e.install("--components", "claude,codex", "--switch", "none"); err != nil {
		t.Fatalf("install: %v\n%s", err, e.diag.String())
	}

	if u := e.host.user["codex-remote.service"]; u.active || u.enabled {
		t.Fatalf("legacy --user head still enabled=%v active=%v", u.enabled, u.active)
	}
	mustAbsent(t, legacyUnit)
	unit := systemd.CodexUnit(testOperator)
	if u := e.host.system[unit]; u == nil || !u.enabled || !u.active {
		t.Fatalf("Codex head was not enabled and started: %+v", u)
	}
	if !e.host.called("systemctl enable --now " + unit) {
		t.Fatalf("calls:\n%s", strings.Join(e.host.calls, "\n"))
	}
	// The watchdog follows the (re)enabled head on the next install.
	if err := e.install(); err != nil {
		t.Fatal(err)
	}
	if u := e.host.user[systemd.CodexWatchdogTimer]; u == nil || !u.enabled {
		t.Fatal("watchdog timer was not enabled for the enabled Codex head")
	}
}

// The workspaces.conf drop-in the retired sandbox generated is removed on
// install, and its directory when nothing else is in it. A drop-in the
// operator wrote, even at the same path, and a sibling drop-in stay.
func TestInstallRetiresTheGeneratedWorkspacesDropIn(t *testing.T) {
	dropIn := systemd.CodexWorkspacesDropIn(testOperator)
	generated := "[Service]\nBindPaths=/home/operator/alpha\nTemporaryFileSystem=/home/operator/alpha/.claude/worktrees:ro\nBindPaths=/home/operator/beta\nTemporaryFileSystem=/home/operator/beta/.claude/worktrees:ro\n"

	e := newEnv(t)
	writeTestFile(t, e.sys(dropIn.Path), generated, 0o644)
	if err := e.install("--components", "claude,codex", "--switch", "none"); err != nil {
		t.Fatalf("install: %v\n%s", err, e.diag.String())
	}
	mustAbsent(t, e.sys(dropIn.Path))
	mustAbsent(t, filepath.Dir(e.sys(dropIn.Path)))
	if !strings.Contains(e.out.String(), "removed the retired drop-in "+dropIn.Path) {
		t.Fatalf("the removal was not reported:\n%s", e.out.String())
	}

	// Site policy: same path with foreign content stays, so does a sibling.
	e = newEnv(t)
	writeTestFile(t, e.sys(dropIn.Path), "[Service]\nBindPaths=/home/operator/alpha\nPrivateTmp=yes\n", 0o644)
	if err := e.install("--components", "claude,codex", "--switch", "none"); err != nil {
		t.Fatalf("install: %v\n%s", err, e.diag.String())
	}
	if got := readTestFile(t, e.sys(dropIn.Path)); !strings.Contains(got, "PrivateTmp=yes") {
		t.Fatalf("a drop-in codvps did not generate was rewritten: %q", got)
	}
	if !strings.Contains(e.diag.String(), "kept "+dropIn.Path+": it is not a file codvps generated") {
		t.Fatalf("the kept drop-in was not reported:\n%s", e.diag.String())
	}

	e = newEnv(t)
	sibling := e.sys("etc/systemd/system/codex-remote@operator.service.d/site-hardening.conf")
	writeTestFile(t, e.sys(dropIn.Path), generated, 0o644)
	writeTestFile(t, sibling, "[Service]\nNoNewPrivileges=yes\n", 0o644)
	if err := e.install("--components", "claude,codex", "--switch", "none"); err != nil {
		t.Fatalf("install: %v\n%s", err, e.diag.String())
	}
	mustAbsent(t, e.sys(dropIn.Path))
	if got := readTestFile(t, sibling); got != "[Service]\nNoNewPrivileges=yes\n" {
		t.Fatalf("a sibling drop-in was touched: %q", got)
	}

	// A second run finds nothing left to retire.
	e.out.Reset()
	if err := e.install(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.out.String(), "retired drop-in") {
		t.Fatalf("a rerun retired a drop-in again:\n%s", e.out.String())
	}
}

// Install removes the state the retired sandbox left in the operator's
// home: the membership manifest, and a control-socket link dangling into
// the old unit's private /tmp. A live socket path is left alone.
func TestInstallRemovesTheRetiredSandboxState(t *testing.T) {
	e := newEnv(t)
	manifest := filepath.Join(e.home, ".config/codvps/codex-repositories")
	writeTestFile(t, manifest, "codex-one\n", 0o600)
	if err := os.Chmod(filepath.Join(e.home, ".config/codvps"), 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(e.home, ".codex/app-server-control/app-server-control.sock")
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp/systemd-private-gone/tmp/app-server-control.sock", socket); err != nil {
		t.Fatal(err)
	}
	if err := e.install("--components", "claude,codex", "--switch", "none"); err != nil {
		t.Fatalf("install: %v\n%s", err, e.diag.String())
	}
	mustAbsent(t, manifest)
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("the dangling control socket link survived install: %v", err)
	}

	// A link that resolves is not ours to remove.
	target := filepath.Join(e.home, "real.sock")
	writeTestFile(t, target, "", 0o600)
	if err := os.Symlink(target, socket); err != nil {
		t.Fatal(err)
	}
	if err := e.install(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socket); err != nil {
		t.Fatalf("install removed a control socket link that resolves: %v", err)
	}
}

// Without a Codex login the migrated head stays enabled but stopped.
func TestInstallKeepsEnabledCodexHeadStoppedWithoutLogin(t *testing.T) {
	e := newEnv(t)
	e.tool("codex")
	unit := systemd.CodexUnit(testOperator)
	e.host.system[unit] = &fakeUnit{enabled: true, active: true}
	if err := e.install("--components", "codex"); err != nil {
		t.Fatal(err)
	}
	if u := e.host.system[unit]; !u.enabled || u.active {
		t.Fatalf("head = %+v, want enabled and stopped", u)
	}
	if !strings.Contains(e.diag.String(), "Codex Remote remains enabled but stopped") {
		t.Fatalf("missing warning:\n%s", e.diag.String())
	}
}

// Explicit flags win; a single flag keeps the other axis as recorded, and
// a deselection is reported without removing anything.
func TestInstallSelectionRules(t *testing.T) {
	e := newEnv(t)
	e.tool("codex")
	if err := e.install("--components", "claude,codex", "--switch", "none"); err != nil {
		t.Fatal(err)
	}
	if err := e.install("--components=claude"); err != nil {
		t.Fatal(err)
	}
	reg, err := testCatalog.Read(e.sys("etc/codvps/components.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(reg.Selected(), ",") != "claude" || reg.SwitchValue() != "none" {
		t.Fatalf("registry = %v %s", reg.Selected(), reg.SwitchValue())
	}
	if !strings.Contains(e.out.String(), "note: codex was deselected; nothing was removed.") {
		t.Fatalf("missing deselection note:\n%s", e.out.String())
	}

	if err := e.install(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "using existing component selection from /etc/codvps/components.json: coding CLIs=claude switch=none") {
		t.Fatalf("unexpected output:\n%s", e.out.String())
	}
}

// On a fresh host with a terminal the selection is asked for.
func TestInstallPromptsOnATerminal(t *testing.T) {
	e := newEnv(t)
	e.opts.IsTTY = true
	e.opts.Stdin = strings.NewReader("claude\n\n")
	if err := e.install(); err != nil {
		t.Fatal(err)
	}
	data := readTestFile(t, e.sys("etc/codvps/components.json"))
	var reg components.Registry
	if err := json.Unmarshal([]byte(data), &reg); err != nil {
		t.Fatal(err)
	}
	if strings.Join(reg.CodingCLIs, ",") != "claude" || reg.Switch != "none" {
		t.Fatalf("registry = %s", data)
	}
}

// A host codvps installed before, whose registry is gone, is detected
// rather than asked about.
func TestInstallDetectsAnExistingInstallation(t *testing.T) {
	e := newEnv(t)
	e.tool("codex")
	if err := e.install("--components", "codex"); err != nil {
		t.Fatal(err)
	}
	// The vendor install (first enable or login) is what creates ~/.codex.
	if err := os.MkdirAll(filepath.Join(e.home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.sys("etc/codvps/components.json")); err != nil {
		t.Fatal(err)
	}
	if err := e.install(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "detected an existing installation with no component registry; selected coding CLIs=codex switch=none") {
		t.Fatalf("unexpected output:\n%s", e.out.String())
	}
}

// The operator-side state refuses a symlinked state root instead of
// following it.
func TestEnsureOperatorStateRefusesSymlinkedStateRoot(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(filepath.Join(e.home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(e.home, ".config/codvps")); err != nil {
		t.Fatal(err)
	}
	err := e.install("--components", "claude")
	if err == nil || !strings.Contains(err.Error(), "codvps state root creation is unsafe") {
		t.Fatalf("got %v", err)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("install wrote through the symlinked state root: %v", entries)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// An install on a host with the retired pinned runtimes removes their
// drop-ins, units, tmpfiles snippet and trees, so the heads run the
// vendor-installed CLIs, and stops their watchers.
func TestInstallRetiresPinnedRuntimes(t *testing.T) {
	e := newEnv(t)
	e.tool("claude")
	for _, p := range systemd.LegacyRuntimePaths(testOperator) {
		writeTestFile(t, e.sys(p), "[Service]\n", 0o644)
	}
	writeTestFile(t, e.sys("opt/codvps/runtimes/claude/versions/1.0.0/bin/claude"), "", 0o755)
	writeTestFile(t, e.sys("run/codvps/update-requests/x"), "", 0o644)
	if err := e.install("--components", "claude"); err != nil {
		t.Fatalf("install: %v\n%s", err, e.diag.String())
	}
	for _, p := range append(systemd.LegacyRuntimePaths(testOperator), "/opt/codvps/runtimes", "/run/codvps") {
		mustAbsent(t, e.sys(p))
	}
	if !strings.Contains(e.out.String(), "removed the pinned runtime copies") {
		t.Fatalf("output:\n%s", e.out.String())
	}
	// A head still running a copy keeps the tree until it restarts.
	writeTestFile(t, e.sys("opt/codvps/runtimes/claude/versions/1.0.0/bin/claude"), "", 0o755)
	proc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "42"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/opt/codvps/runtimes/claude/versions/1.0.0/bin/claude (deleted)", filepath.Join(proc, "42", "exe")); err != nil {
		t.Fatal(err)
	}
	e.opts.ProcRoot = proc
	if err := e.install(); err != nil {
		t.Fatalf("reinstall: %v\n%s", err, e.diag.String())
	}
	if _, err := os.Stat(e.sys("opt/codvps/runtimes/claude/versions/1.0.0/bin/claude")); err != nil {
		t.Fatal("install removed a runtime tree a running head executes from")
	}
	if !strings.Contains(e.diag.String(), "1 running process(es) still execute from it") {
		t.Fatalf("diag:\n%s", e.diag.String())
	}
	stopped := strings.Join(e.host.calls, "\n")
	for _, u := range systemd.LegacyRuntimeUnits(testOperator) {
		if !strings.Contains(stopped, "systemctl disable --now "+u) {
			t.Errorf("%s was not stopped", u)
		}
	}
}
