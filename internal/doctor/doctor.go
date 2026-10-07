// Package doctor implements `codvps doctor`: every prerequisite
// and health check of the reference's doctor_report, printed as one
// PASS/WARN/FAIL line each, followed by a failure count.
//
// Exit codes: 0 when no check failed, 1 when at least one did, and 2 when
// doctor could not run its checks at all (an unreadable repository or
// component registry), so a script can tell "unhealthy" from "could not
// tell".
//
// The host-wide checks (node, GitHub, identity, tools, AppArmor, the state
// root, the user manager, the update machinery) are here; each provider's
// own checks are a Suite (claude.go, codex.go) that its
// definition in internal/providers hands to doctor as a Component, and Run
// calls in fixed phases. The caller reads the component registry; doctor
// itself never branches on a provider's name.
//
// A component that is not selected is absent by design, so its checks are
// skipped with a WARN rather than failed. No check ever prints a secret:
// the Anthropic variables are reported as set/unset, never by value, and
// credential files only by presence and shape.
//
// It warns while leftovers of the retired pinned runtimes are on disk. Not ported: the personal-config checkout check belongs to
// the removed heritage command.
package doctor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egginsect/codvps/internal/configlink"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/systemd"
)

// ApparmorProfile is the bubblewrap AppArmor profile the codvps installer
// provisions when the distro does not ship one.
const ApparmorProfile = "/etc/apparmor.d/codvps-bwrap-userns-restrict"

// distroApparmorProfile is Ubuntu's own profile, accepted when dpkg owns it.
const distroApparmorProfile = "/etc/apparmor.d/bwrap-userns-restrict"

// nodePath is where the claude CLI's runtime must be for the systemd
// units, which do not source nvm shims.
const nodePath = "/usr/bin/node"

// baseTools must resolve on the operator's PATH.
var baseTools = []string{"jq", "rg", "uv", "pnpm", "bwrap"}

// Suite is one provider's doctor checks, built by its definition in
// internal/providers. Every phase is optional.
type Suite struct {
	// Tools must resolve on the operator's PATH while the provider is
	// selected.
	Tools []string
	// Prepare reads, before the first line is printed, what can make
	// doctor unable to answer (so a report is either complete or not
	// printed at all). It runs whether or not the provider is selected.
	Prepare func(c *Check) error
	// Install checks the provider's CLI, login, install and runtime, with
	// the prerequisites, for a selected provider.
	Install func(c *Check)
	// Health checks the provider's running service, after the update
	// machinery and the state root, for a selected provider.
	Health func(c *Check)
	// Head checks the provider's Remote Control head at the end. It runs
	// whether or not the provider is selected: a head enabled before a
	// deselection keeps running.
	Head func(c *Check)
}

// Component is one provider as doctor sees it.
type Component struct {
	// Name is the provider's name.
	Name string
	// Selected is whether the component registry selects it.
	Selected bool
	// Optional components (a switch) are skipped silently when not
	// selected; a coding CLI that is not selected is reported as skipped.
	Optional bool
	// Suite are its checks.
	Suite Suite
}

// Options are doctor's injected dependencies.
type Options struct {
	// Layout resolves HOME and the codvps state paths; it carries the
	// system root tests use for /etc.
	Layout *paths.Layout
	// Root prefixes the other system paths doctor reads (/usr/bin/node,
	// /usr/local/bin/codvps, /etc/apparmor.d, /etc/systemd); "" in
	// production.
	Root string
	// Runner runs systemctl, journalctl, loginctl, gh, git, the coding
	// CLIs and dpkg-query.
	Runner runner.Runner
	// Operator is the account doctor reports on.
	Operator string
	// ProcRoot is where process environments are read ("/proc" when empty).
	ProcRoot string
	// LookupEnv reads the caller's environment (only to test presence).
	LookupEnv func(string) (string, bool)
	// LookPath resolves a command on the operator's PATH.
	LookPath func(string) (string, error)
	// CgroupRoot is the cgroup filesystem ("/sys/fs/cgroup" when empty).
	CgroupRoot string
	// ShellPath captures the PATH the operator's login shell gives a head
	// (`codvps internal shell-exec`) when it starts from base, the
	// environment the head's unit gives the launcher. When nil, the head
	// PATH check is skipped.
	ShellPath func(base []string) (string, error)
	// Out receives the report.
	Out io.Writer
	// Components are the providers, in provider order, with their
	// selection and checks.
	Components []Component
}

// Report counts the lines doctor printed.
type Report struct {
	Failures int
}

// Check is one doctor run: the report every check writes into, and the
// host facts the checks read.
type Check struct {
	opts     Options
	out      io.Writer
	failures int
	// shellPaths caches the login shell's PATH per starting PATH.
	shellPaths map[string]shellPathResult
	// shellPathWarned is set once a capture failure has been reported.
	shellPathWarned bool
}

type shellPathResult struct {
	path string
	err  error
}

// loginShellPath is the PATH the login shell gives a head whose unit
// starts the launcher with unitPATH. The capture runs once per distinct
// base environment; a failure is reported (as a WARN) the first time only.
func (d *Check) loginShellPath(unit string) (string, bool) {
	unitPATH, err := systemd.LaunchPATH(unit, d.home(), d.opts.Operator)
	if err != nil {
		d.Warn("cannot tell the launch PATH of %s (%v)", unit, err)
		return "", false
	}
	res, done := d.shellPaths[unitPATH]
	if !done {
		base := []string{
			"HOME=" + d.home(),
			"USER=" + d.opts.Operator,
			"LOGNAME=" + d.opts.Operator,
			"PATH=" + unitPATH,
		}
		res.path, res.err = d.opts.ShellPath(base)
		if d.shellPaths == nil {
			d.shellPaths = map[string]shellPathResult{}
		}
		d.shellPaths[unitPATH] = res
		if res.err != nil && !d.shellPathWarned {
			d.shellPathWarned = true
			d.Warn("cannot capture the login-shell PATH to compare with the heads (%v)", res.err)
		}
	}
	return res.path, res.err == nil
}

// checkHeadShellPath compares an active head's PATH (read from
// /proc/<MainPID>/environ) with the login shell's, so a head started
// before the shell environment was wired in is found. A unit with no main
// process (a oneshot whose launcher exited) is checked through every
// top-level process left in its cgroup. user says which systemd manager
// runs the unit.
func (d *Check) checkHeadShellPath(user bool, unit string) {
	if d.opts.ShellPath == nil {
		return
	}
	pid, _ := strconv.Atoi(repo.ShowUnitProperties(d.opts.Runner, user, unit, "MainPID")["MainPID"])
	if pid > 0 {
		d.checkPIDsShellPath(unit, []int{pid})
		return
	}
	d.checkPIDsShellPath(unit, d.cgroupRoots(user, unit))
}

// checkPIDsShellPath passes when every pid runs with the login-shell PATH
// and warns about the first that does not. No pid is a warning.
func (d *Check) checkPIDsShellPath(unit string, pids []int) {
	if d.opts.ShellPath == nil {
		return
	}
	want, ok := d.loginShellPath(unit)
	if !ok {
		return
	}
	if len(pids) == 0 {
		d.Warn("%s is not running with the login-shell PATH; restart it (no main process found)", unit)
		return
	}
	for _, pid := range pids {
		data, err := os.ReadFile(filepath.Join(d.procRoot(), strconv.Itoa(pid), "environ"))
		if err != nil {
			d.Warn("%s is not running with the login-shell PATH; restart it (cannot read the environment of process %d: %v)", unit, pid, err)
			return
		}
		got := ""
		for _, e := range strings.Split(string(data), "\x00") {
			if v, found := strings.CutPrefix(e, "PATH="); found {
				got = v
			}
		}
		// A CLI may prepend its own directories to the PATH it was given.
		if got != want && !strings.HasSuffix(got, ":"+want) {
			if len(pids) == 1 {
				d.Warn("%s is not running with the login-shell PATH; restart it", unit)
			} else {
				d.Warn("%s is not running with the login-shell PATH; restart it (process %d)", unit, pid)
			}
			return
		}
	}
	d.Pass("%s runs with the login-shell PATH", unit)
}

// cgroupRoots lists the unit's top-level processes from its control group:
// those whose parent is outside the group, in cgroup order.
func (d *Check) cgroupRoots(user bool, unit string) []int {
	cg := repo.ShowUnitProperties(d.opts.Runner, user, unit, "ControlGroup")["ControlGroup"]
	if cg == "" || cg == "/" {
		return nil
	}
	root := d.opts.CgroupRoot
	if root == "" {
		root = "/sys/fs/cgroup"
	}
	data, err := os.ReadFile(filepath.Join(root, cg, "cgroup.procs"))
	if err != nil {
		return nil
	}
	members := map[int]bool{}
	var pids []int
	for _, f := range strings.Fields(string(data)) {
		if pid, err := strconv.Atoi(f); err == nil && pid > 0 {
			members[pid] = true
			pids = append(pids, pid)
		}
	}
	var roots []int
	for _, pid := range pids {
		if !members[d.parentPID(pid)] {
			roots = append(roots, pid)
		}
	}
	return roots
}

// parentPID is the parent of pid from /proc/<pid>/stat, or 0.
func (d *Check) parentPID(pid int) int {
	data, err := os.ReadFile(filepath.Join(d.procRoot(), strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	// "pid (comm) state ppid ...": comm may hold spaces and parentheses.
	s := string(data)
	rest := strings.Fields(s[strings.LastIndex(s, ")")+1:])
	if len(rest) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(rest[1])
	return ppid
}

func (d *Check) procRoot() string {
	if d.opts.ProcRoot == "" {
		return "/proc"
	}
	return d.opts.ProcRoot
}

// Pass reports a check that passed.
func (d *Check) Pass(format string, args ...any) {
	_, _ = fmt.Fprintf(d.out, "PASS: "+format+"\n", args...)
}

// Warn reports a problem that does not fail doctor.
func (d *Check) Warn(format string, args ...any) {
	_, _ = fmt.Fprintf(d.out, "WARN: "+format+"\n", args...)
}

// Fail reports a failed check.
func (d *Check) Fail(format string, args ...any) {
	_, _ = fmt.Fprintf(d.out, "FAIL: "+format+"\n", args...)
	d.failures++
}

func (d *Check) sys(path string) string { return filepath.Join(d.opts.Root, path) }
func (d *Check) home() string           { return d.opts.Layout.Home() }

// Run prints the report and returns the failure count. An error means the
// checks could not run (exit 2); a non-zero Failures means they ran and
// found problems (exit 1).
func Run(opts Options) (Report, error) {
	switch {
	case opts.Layout == nil || opts.Runner == nil:
		return Report{}, errors.New("doctor: Layout and Runner are required")
	case opts.LookupEnv == nil || opts.LookPath == nil || opts.Out == nil:
		return Report{}, errors.New("doctor: LookupEnv, LookPath and Out are required")
	case opts.Operator == "":
		return Report{}, errors.New("doctor: Operator is required")
	}
	d := &Check{opts: opts, out: opts.Out}

	// Read everything that can make doctor unable to answer before the
	// first line, so a report is either complete or not printed at all.
	for _, c := range opts.Components {
		if c.Suite.Prepare == nil {
			continue
		}
		if err := c.Suite.Prepare(d); err != nil {
			return Report{}, err
		}
	}

	d.checkNode()
	d.checkGitHub()
	d.checkIdentity()
	d.checkTools()
	d.checkApparmor()
	for _, c := range opts.Components {
		switch {
		case c.Selected && c.Suite.Install != nil:
			c.Suite.Install(d)
		case !c.Selected && !c.Optional:
			d.Warn("%s is not a selected component; skipping its checks", c.Name)
		}
	}
	d.checkLegacyRuntimes()
	stateDir := opts.Layout.ConfigDir()
	if privateOperatorPath(stateDir, true, 0o700) {
		d.Pass("codvps state root is a private operator-owned directory")
	} else {
		d.Fail("codvps state root must be an operator-owned mode-700 directory: %s", stateDir)
	}
	for _, c := range opts.Components {
		if c.Selected && c.Suite.Health != nil {
			c.Suite.Health(d)
		}
	}
	d.checkUserManager()
	d.checkConfigLink()
	for _, c := range opts.Components {
		if c.Suite.Head != nil {
			c.Suite.Head(d)
		}
	}

	if d.failures > 0 {
		_, _ = fmt.Fprintf(d.out, "Doctor found %d failure(s).\n", d.failures)
	} else {
		_, _ = fmt.Fprintln(d.out, "Doctor found no failures.")
	}
	return Report{Failures: d.failures}, nil
}

// checkConfigLink reports a linked config repo whose mirrored files are no
// longer links into the checkout: a tool that rewrote a file replaced its
// link, or the repo gained a file since the last `codvps config link`.
func (d *Check) checkConfigLink() {
	checkout, err := configlink.Linked(d.opts.Layout.ConfigDir())
	switch {
	case err != nil:
		d.Warn("cannot read the linked config repo: %v", err)
		return
	case checkout == "":
		return
	}
	replaced, missing, err := configlink.Drift(d.home(), checkout)
	if err != nil {
		d.Warn("cannot check the linked config repo %s: %v", checkout, err)
		return
	}
	for _, p := range replaced {
		d.Warn("%s is no longer a link into %s (a tool rewrote it); commit any change to the repo, then run codvps config link %s", p, checkout, filepath.Base(checkout))
	}
	for _, p := range missing {
		d.Warn("%s from the config repo is not linked yet; run codvps config link %s", p, filepath.Base(checkout))
	}
	if len(replaced) == 0 && len(missing) == 0 {
		d.Pass("config repo %s is linked", checkout)
	}
}

func (d *Check) checkNode() {
	if isExecutableFile(d.sys(nodePath)) {
		d.Pass("node is present at %s", nodePath)
	} else {
		d.Fail("node is missing at %s", nodePath)
	}
}

func (d *Check) checkGitHub() {
	if _, err := d.opts.LookPath("gh"); err != nil {
		d.Fail("gh is missing")
		return
	}
	d.Pass("gh is present")
	if d.succeeds("gh", "auth", "status") {
		d.Pass("GitHub authentication is configured")
	} else {
		d.Warn("GitHub authentication is not configured; run codvps login github")
	}
}

func (d *Check) checkIdentity() {
	if d.gitConfig("user.name") != "" && d.gitConfig("user.email") != "" {
		d.Pass("git identity is configured")
	} else {
		d.Fail("git identity is incomplete; run: codvps login github")
	}
	if fileExists(filepath.Join(d.home(), ".ssh", "id_ed25519")) {
		d.Pass("SSH key ~/.ssh/id_ed25519 is present")
	} else {
		d.Warn("SSH key ~/.ssh/id_ed25519 is missing; run codvps login github")
	}
	if knownHostsHasGitHub(filepath.Join(d.home(), ".ssh", "known_hosts")) {
		d.Pass("github.com is present in ~/.ssh/known_hosts")
	} else {
		d.Warn("github.com is missing from ~/.ssh/known_hosts; run: codvps login github")
	}
	if d.gitConfig("url.git@github.com:.insteadOf") == "https://github.com/" {
		d.Pass("GitHub HTTPS remotes rewrite to SSH")
	} else {
		d.Warn("GitHub HTTPS-to-SSH rewrite is missing; run: codvps login github")
	}
}

func (d *Check) gitConfig(key string) string {
	out, _, code, err := d.opts.Runner.Run("git", "config", "--global", "--get", key)
	if code != 0 || err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func knownHostsHasGitHub(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), "github.com ")
}

// checkTools requires the base tools and every selected provider's own on
// the operator's PATH.
func (d *Check) checkTools() {
	tools := append([]string(nil), baseTools...)
	for _, c := range d.opts.Components {
		if c.Selected {
			tools = append(tools, c.Suite.Tools...)
		}
	}
	for _, tool := range tools {
		if _, err := d.opts.LookPath(tool); err == nil {
			d.Pass("%s is present", tool)
		} else {
			d.Fail("%s is missing", tool)
		}
	}
}

func (d *Check) checkApparmor() {
	if fileExists(d.sys(ApparmorProfile)) ||
		(fileExists(d.sys(distroApparmorProfile)) && d.succeeds("dpkg-query", "-S", distroApparmorProfile)) {
		d.Pass("bubblewrap AppArmor profile is installed")
	} else {
		d.Fail("bubblewrap AppArmor profile is missing")
	}
}

// checkUserManager covers linger and the systemd user bus, which every
// Claude head and the Codex watchdog depend on.
func (d *Check) checkUserManager() {
	linger := ""
	if out, _, code, err := d.opts.Runner.Run("loginctl", "show-user", d.opts.Operator, "-p", "Linger", "--value"); code == 0 && err == nil {
		linger = strings.TrimSpace(out)
	}
	if linger == "yes" {
		d.Pass("Linger=yes")
	} else {
		d.Fail("Linger is %s; run loginctl enable-linger %s", orDefault(linger, "unavailable"), d.opts.Operator)
	}

	// An empty answer (no bus at all) is handled explicitly below.
	out, _, _, _ := d.opts.Runner.Run("systemctl", "--user", "is-system-running")
	switch bus := strings.TrimSpace(out); bus {
	case "running", "degraded":
		d.Pass("systemd user bus is reachable (%s)", bus)
	case "":
		// No answer at all usually means no login session: pam_systemd is
		// what exports XDG_RUNTIME_DIR, and without it systemctl --user
		// has no socket. Name that, but still fail: every user-unit
		// command codvps runs is broken in such a shell.
		if v, ok := d.opts.LookupEnv("XDG_RUNTIME_DIR"); !ok || v == "" {
			d.Fail("systemd user bus is unreachable because XDG_RUNTIME_DIR is unset; run codvps from a login shell (ssh, or sudo -iu %s)", d.opts.Operator)
		} else {
			d.Fail("systemd user bus is not healthy (unreachable)")
		}
	default:
		d.Fail("systemd user bus is not healthy (%s)", bus)
	}
}

func (d *Check) succeeds(name string, args ...string) bool {
	_, _, code, err := d.opts.Runner.Run(name, args...)
	return code == 0 && err == nil
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

func isExecutableFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

func fileHasLine(path, want string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == want {
			return true
		}
	}
	return false
}

func orUnknown(v string) string { return orDefault(v, "unknown") }

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// checkLegacyRuntimes warns while the removed managed-runtime layer is
// still on disk: its drop-ins would keep a head on a copied release
// instead of the vendor-installed CLI.
func (d *Check) checkLegacyRuntimes() {
	for _, path := range systemd.LegacyRuntimePaths(d.opts.Operator) {
		if _, err := os.Lstat(d.sys(path)); err == nil {
			d.Warn("pinned runtime leftover %s is present; rerun sudo codvps install to remove it", path)
		}
	}
	// The layout already resolves the release tree under the system root.
	if tree := d.opts.Layout.RuntimeRootPath(); dirExistsAt(tree) {
		d.Warn("pinned runtime copies are still in %s; rerun sudo codvps install to remove them", tree)
	}
}

func dirExistsAt(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.IsDir()
}
