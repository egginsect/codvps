package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/systemd"
)

// installedEnv is a host after a full claude+codex install with a running
// Claude head, an enabled Codex head, and operator state worth keeping.
func installedEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	for _, tool := range []string{"uv", "uvx", "codex"} {
		e.tool(tool)
	}
	if err := e.install("--components", "claude,codex", "--switch", "none"); err != nil {
		t.Fatalf("install: %v\n%s", err, e.diag.String())
	}
	e.host.user["claude-remote@hello.service"] = &fakeUnit{enabled: true, active: true}
	e.host.user[systemd.CodexWatchdogTimer] = &fakeUnit{enabled: true, active: true}
	e.host.system[systemd.CodexUnit(testOperator)] = &fakeUnit{enabled: true, active: true}
	e.checkout("hello")
	writeTestFile(t, filepath.Join(e.home, ".config/codvps/repositories"), "hello\n", 0o600)
	writeTestFile(t, filepath.Join(e.home, ".codex/auth.json"), "{}", 0o600)
	writeTestFile(t, filepath.Join(e.home, ".claude/.credentials.json"), "{}", 0o600)
	return e
}

// test_uninstall_and_reinstall_round_trip: --dry-run changes nothing;
// uninstall stops the heads and removes exactly the declared set of
// codvps artifacts, keeping other files, operator state and credentials;
// a second run is a no-op; install brings the host back.
func TestUninstallAndReinstallRoundTrip(t *testing.T) {
	e := installedEnv(t)
	foreignDropIn := e.sys("etc/systemd/system/codex-remote@operator.service.d/site-hardening.conf")
	writeTestFile(t, foreignDropIn, "[Service]\nNoNewPrivileges=yes\n", 0o644)
	// A workspaces.conf an earlier codvps generated and install did not get
	// to retire (uninstall on a host that was never upgraded).
	generatedDropIn := systemd.CodexWorkspacesDropIn(testOperator).Path
	writeTestFile(t, e.sys(generatedDropIn), "[Service]\nBindPaths=/home/operator/hello\n", 0o644)
	decoy := e.sys("usr/local/bin/rg")
	writeTestFile(t, decoy, "#!/bin/sh\n", 0o755)

	before := tree(t, e.root) + tree(t, e.home)
	if err := e.uninstall("--dry-run"); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if after := tree(t, e.root) + tree(t, e.home); after != before {
		t.Fatalf("--dry-run changed state:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for _, want := range []string{"DRY-RUN: no changes will be made.", "DRY-RUN: would stop and disable user unit claude-remote@hello.service", "DRY-RUN: would remove /usr/local/bin/codvps", "DRY-RUN complete; nothing was removed."} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("dry run output lacks %q:\n%s", want, e.out.String())
		}
	}
	if u := e.host.user["claude-remote@hello.service"]; !u.active {
		t.Fatal("--dry-run stopped a Claude head")
	}

	if err := e.uninstall(); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, e.diag.String())
	}
	if !strings.Contains(e.out.String(), "Uninstall complete.") {
		t.Fatalf("output:\n%s", e.out.String())
	}
	for _, p := range append([]string{systemd.CodvpsBinary, "/usr/local/bin/uv", "/usr/local/bin/uvx", "/usr/local/bin/codex", generatedDropIn,
		"/opt/codvps/runtimes", "/run/codvps"}, systemd.LegacyRuntimePaths(testOperator)...) {
		mustAbsent(t, e.sys(p))
	}
	for _, u := range ProductUnits(providers.All()) {
		mustAbsent(t, e.sys(u.InstallPath))
	}
	if u := e.host.user["claude-remote@hello.service"]; u.active || u.enabled {
		t.Fatal("uninstall left a Claude head running")
	}
	if u := e.host.system[systemd.CodexUnit(testOperator)]; u.active || u.enabled {
		t.Fatal("uninstall left the Codex head running")
	}
	if u := e.host.user[systemd.CodexWatchdogTimer]; u.active || u.enabled {
		t.Fatal("uninstall left the watchdog running")
	}
	// Foreign files and operator state survive.
	for _, keep := range []string{foreignDropIn, decoy, e.sys("etc/codvps/components.json"),
		filepath.Join(e.home, ".config/codvps/repositories"),
		filepath.Join(e.home, ".codex/auth.json"), filepath.Join(e.home, ".claude/.credentials.json"),
		filepath.Join(e.home, "hello/.git")} {
		if _, err := os.Lstat(keep); err != nil {
			t.Errorf("uninstall removed %s", keep)
		}
	}

	// A second run is a clean no-op.
	before = tree(t, e.root) + tree(t, e.home)
	if err := e.uninstall(); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	for _, want := range []string{"/usr/local/bin/codvps: already absent.", "/etc/systemd/user/claude-remote@.service: already absent.", "/etc/systemd/user/claude-remote.slice: already absent.", "Uninstall complete."} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("second run lacks %q:\n%s", want, e.out.String())
		}
	}
	if after := tree(t, e.root) + tree(t, e.home); after != before {
		t.Fatalf("second uninstall changed state")
	}

	// install is the way back.
	if err := e.install(); err != nil {
		t.Fatalf("reinstall: %v\n%s", err, e.diag.String())
	}
	for _, u := range ProductUnits(providers.All()) {
		if got := readTestFile(t, e.sys(u.InstallPath)); got != string(u.Content) {
			t.Fatalf("reinstall did not restore %s", u.InstallPath)
		}
	}
	if _, err := os.Readlink(e.sys("usr/local/bin/uv")); err != nil {
		t.Fatal("reinstall did not restore the uv link")
	}
}

// Uninstall removes the declared set whatever put it there -- a unit file
// at a codvps path is codvps's -- but a /usr/local/bin entry that is not
// the link codvps makes (a repointed link, a real binary) is reported and
// kept.
func TestUninstallRemovesTheDeclaredSetAndKeepsOtherLinks(t *testing.T) {
	e := newEnv(t)
	foreignUnit := e.sys("etc/systemd/user/claude-remote.slice")
	writeTestFile(t, foreignUnit, "[Slice]\n", 0o644)
	repointed := e.sys("usr/local/bin/uvx")
	if err := os.MkdirAll(filepath.Dir(repointed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/opt/elsewhere/uvx", repointed); err != nil {
		t.Fatal(err)
	}
	decoy := e.sys("usr/local/bin/uv")
	writeTestFile(t, decoy, "#!/bin/sh\n", 0o755)

	if err := e.uninstall(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	mustAbsent(t, foreignUnit)
	if target, _ := os.Readlink(repointed); target != "/opt/elsewhere/uvx" {
		t.Fatal("uninstall removed a repointed link")
	}
	if _, err := os.Lstat(decoy); err != nil {
		t.Fatal("uninstall removed a root-owned binary it did not install")
	}
	for _, want := range []string{
		"skipping /usr/local/bin/uv: not a symlink, so it is not the link codvps makes",
		"skipping /usr/local/bin/uvx: points at '/opt/elsewhere/uvx'",
	} {
		if !strings.Contains(e.diag.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, e.diag.String())
		}
	}
}

func TestUninstallUsageAndOperator(t *testing.T) {
	e := newEnv(t)
	if err := e.uninstall("--force"); err == nil || !strings.Contains(err.Error(), UninstallUsage) {
		t.Fatalf("got %v", err)
	}
	e.opts.SudoUser = "root"
	if err := e.uninstall(); err == nil || !strings.Contains(err.Error(), "run the uninstaller as a normal sudo-capable user") {
		t.Fatalf("got %v", err)
	}
}

// A failed removal is reported as an incomplete uninstall (exit non-zero)
// after every other step has still run; what could not be removed stays,
// and a rerun removes it.
func TestUninstallReportsRemovalFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	e := installedEnv(t)
	bin := e.sys("usr/local/bin")
	if err := os.Chmod(bin, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bin, 0o755) })
	err := e.uninstall()
	if err == nil || !strings.Contains(err.Error(), "uninstall incomplete") {
		t.Fatalf("got %v", err)
	}
	for _, u := range ProductUnits(providers.All()) {
		mustAbsent(t, e.sys(u.InstallPath))
	}
	if _, err := os.Lstat(e.sys(systemd.CodvpsBinary)); err != nil {
		t.Fatalf("the binary that could not be removed is gone: %v", err)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.uninstall(); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	mustAbsent(t, e.sys(systemd.CodvpsBinary))
}

// A link someone replaced with a real binary is not codvps's link, so
// uninstall keeps it; install, the single manager of that path, puts the
// link back.
func TestReplacedLinkIsKeptByUninstallAndRestoredByInstall(t *testing.T) {
	e := installedEnv(t)
	uv := e.sys("usr/local/bin/uv")
	if err := os.Remove(uv); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, uv, "#!/bin/sh\n# root-owned decoy\n", 0o755)
	if err := e.uninstall(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.diag.String(), "skipping /usr/local/bin/uv: not a symlink, so it is not the link codvps makes") {
		t.Fatalf("stderr:\n%s", e.diag.String())
	}
	if got := readTestFile(t, uv); !strings.Contains(got, "decoy") {
		t.Fatalf("uninstall removed a real binary: %q", got)
	}
	if err := e.install(); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(uv); err != nil || target != filepath.Join(e.home, ".local/bin/uv") {
		t.Fatalf("reinstall did not restore the link: %q (%v)", target, err)
	}
}
