package install

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// provisionFake extends fakeHost with the provisioning commands. Nothing
// here executes a real binary: each command only records itself and
// creates the files the real one would, inside the test root and home.
type provisionFake struct {
	*fakeHost
	e          *env
	io         []string
	swapActive bool
	failApt    bool
	distroBwrp bool
	oldGH      bool
}

func newProvisionEnv(t *testing.T) (*env, *provisionFake) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	e := newEnv(t)
	f := &provisionFake{fakeHost: e.host, e: e}
	e.opts.Runner = f
	e.opts.Provision = true
	return e, f
}

func (f *provisionFake) Run(name string, args ...string) (string, string, int, error) {
	switch name {
	case "systemd-detect-virt":
		f.calls = append(f.calls, name)
		return "none\n", "", 1, nil
	case "dpkg-query":
		f.calls = append(f.calls, name)
		if f.distroBwrp {
			return "apparmor: " + distroBwrapProfile + "\n", "", 0, nil
		}
		return "", "no path found", 1, nil
	case "gh": // the `auth status --json` probe
		if f.oldGH {
			return "", "unknown flag: --json\n", 1, nil
		}
		return `{"hosts":{}}`, "", 0, nil
	case "dpkg":
		return "amd64\n", "", 0, nil
	case "aa-enabled":
		return "Yes\n", "", 0, nil
	case "swapon":
		if f.swapActive {
			return swapFile + "\n", "", 0, nil
		}
		return "", "", 0, nil
	case "sshd":
		return "port 2222\nport 22\n", "", 0, nil
	}
	return f.fakeHost.Run(name, args...)
}

func (f *provisionFake) RunWithIO(name string, args []string, _ io.Reader, _, _ io.Writer) (int, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.io = append(f.io, line)
	e := f.e
	write := func(p, content string, mode os.FileMode) { writeTestFile(e.t, p, content, mode) }
	switch name {
	case "env": // env DEBIAN_FRONTEND=noninteractive apt-get ...
		if f.failApt && contains(args, "install") {
			return 100, nil
		}
		if contains(args, "nodejs") {
			write(e.sys(nodePath), "node", 0o755)
		}
		if contains(args, "gh") {
			write(e.sys("/usr/bin/gh"), "gh", 0o755)
		}
	case "sh": // sh -c 'cd -- "$1" && exec apt-get download ...' sh <dir>
		write(filepath.Join(args[3], "apparmor-profiles_4.0_all.deb"), "deb", 0o644)
	case "dpkg-deb":
		write(filepath.Join(args[2], bwrapProfileInDeb), "profile bwrap-test {}\n", 0o644)
	case "curl":
		for i, a := range args {
			if a == "-o" {
				write(args[i+1], "downloaded:"+args[len(args)-1], 0o644)
			}
		}
	case "corepack":
		if args[0] == "enable" {
			write(e.sys("/usr/bin/pnpm"), "pnpm", 0o755)
		}
	case "npm":
		write(e.sys("/usr/bin/claude"), "claude", 0o755)
	case "runuser":
		if strings.Contains(line, "CODEX_HOME=") {
			write(filepath.Join(e.home, ".codex/packages/standalone/current/codex"), "codex", 0o755)
			e.tool("codex")
		} else {
			e.tool("uv")
			e.tool("uvx")
		}
	case "fallocate":
		write(args[len(args)-1], "", 0o644)
	case "swapon":
		f.swapActive = true
	case "bash", "mkswap", "ufw", "apparmor_parser":
	default:
		e.t.Fatalf("unexpected provisioning command: %s", line)
	}
	return 0, nil
}

func (f *provisionFake) ran(prefix string) bool {
	for _, c := range f.io {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func (f *provisionFake) index(prefix string) int {
	for i, c := range f.io {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func writeFstab(e *env) {
	writeTestFile(e.t, e.sys(fstab), "UUID=abc / ext4 defaults 0 1\n", 0o644)
}

// A fresh host gets every provisioning step, each shown before it runs,
// and install finishes with the vendor CLIs linked onto the unit PATH.
func TestProvisionFreshHost(t *testing.T) {
	e, f := newProvisionEnv(t)
	writeFstab(e)
	if err := e.install("--components", "claude,codex", "--switch", "none"); err != nil {
		t.Fatalf("install: %v\n%s\n%s", err, e.out.String(), e.diag.String())
	}
	out := e.out.String()
	plan := strings.Index(out, "Host provisioning (--skip-provision skips it):")
	first := strings.Index(out, "+ env DEBIAN_FRONTEND=noninteractive apt-get update")
	if plan < 0 || first < 0 || plan > first {
		t.Fatalf("the plan was not shown before the first command:\n%s", out)
	}
	for _, want := range []string{
		"env DEBIAN_FRONTEND=noninteractive apt-get update",
		"env DEBIAN_FRONTEND=noninteractive apt-get install -y git openssh-client curl ca-certificates jq ripgrep bubblewrap apparmor iproute2",
		"sh -c cd -- \"$1\" && exec apt-get download -o APT::Sandbox::User=root apparmor-profiles sh ",
		"apparmor_parser -r " + e.sys(BwrapProfile),
		"curl -fsSL --proto =https -o ",
		"env DEBIAN_FRONTEND=noninteractive apt-get install -y gh",
		"env DEBIAN_FRONTEND=noninteractive apt-get install -y nodejs",
		"corepack prepare pnpm@latest --activate",
		"fallocate -l 4G " + e.sys(swapFile),
		"mkswap " + e.sys(swapFile),
		"swapon " + e.sys(swapFile),
		"ufw allow OpenSSH",
		"ufw allow 2222",
		"ufw allow 22",
		"ufw default deny incoming",
		"ufw --force enable",
		"runuser -u operator -- env -i HOME=" + e.home + " PATH=/usr/local/bin:/usr/bin:/bin bash -o pipefail -c curl -LsSf --proto =https " + uvInstallerURL + " | sh",
	} {
		if !f.ran(want) {
			t.Errorf("did not run %q; ran:\n%s", want, strings.Join(f.io, "\n"))
		}
		if !strings.Contains(out, "+ "+want) {
			t.Errorf("did not show %q before running it", want)
		}
	}
	// ufw allows SSH before it denies incoming traffic.
	if f.index("ufw allow 2222") > f.index("ufw default deny incoming") {
		t.Errorf("ufw denied incoming before allowing SSH: %v", f.io)
	}
	// Operator installers (uv) run only after the system steps.
	if f.index("runuser") < f.index("ufw --force enable") {
		t.Errorf("operator installers ran before the system steps: %v", f.io)
	}
	// Coding CLIs are installed on first use, never by install.
	for _, never := range []string{"claude.ai/install.sh", "chatgpt.com/codex/install.sh"} {
		if f.ran(never) {
			t.Errorf("install downloaded a coding CLI: %s", never)
		}
	}
	for _, skipped := range []string{"skipped (installed on first codvps enable claude or login claude)", "skipped (installed on first codvps enable codex or login codex)"} {
		if !strings.Contains(out, skipped) {
			t.Errorf("plan does not say %q:\n%s", skipped, out)
		}
	}
	if got := readTestFile(t, e.sys(BwrapProfile)); got != "profile bwrap-test {}\n" {
		t.Errorf("bwrap profile = %q", got)
	}
	mustMode(t, e.sys(BwrapProfile), 0o644)
	if got := readTestFile(t, e.sys(githubSourcesList)); got != "deb [arch=amd64 signed-by="+githubKeyring+"] https://cli.github.com/packages stable main\n" {
		t.Errorf("github-cli.list = %q", got)
	}
	mustMode(t, e.sys(swapFile), 0o600)
	if got := readTestFile(t, e.sys(fstab)); !strings.HasSuffix(got, "\n/swapfile none swap sw 0 0\n") {
		t.Errorf("fstab = %q", got)
	}
	if target, _ := os.Readlink(e.sys("/usr/local/bin/codex")); target != filepath.Join(e.home, ".local/bin/codex") {
		t.Errorf("the Codex CLI is not linked onto the unit PATH: %q", target)
	}
}

// A rerun on a provisioned host fetches and installs nothing again and
// never duplicates the fstab entry.
func TestProvisionRerunIsIdempotent(t *testing.T) {
	e, f := newProvisionEnv(t)
	writeFstab(e)
	if err := e.install("--components", "claude,codex", "--switch", "none"); err != nil {
		t.Fatalf("first install: %v", err)
	}
	before := readTestFile(t, e.sys(BwrapProfile)) + readTestFile(t, e.sys(githubSourcesList)) + readTestFile(t, e.sys(fstab))
	f.io = nil
	if err := e.install(); err != nil {
		t.Fatalf("rerun: %v\n%s", err, e.diag.String())
	}
	for _, never := range []string{"curl", "sh -c", "dpkg-deb", "bash", "npm", "corepack", "runuser", "fallocate", "mkswap", "swapon",
		"env DEBIAN_FRONTEND=noninteractive apt-get install -y gh", "env DEBIAN_FRONTEND=noninteractive apt-get install -y nodejs"} {
		if f.ran(never) {
			t.Errorf("rerun ran %q again: %v", never, f.io)
		}
	}
	after := readTestFile(t, e.sys(BwrapProfile)) + readTestFile(t, e.sys(githubSourcesList)) + readTestFile(t, e.sys(fstab))
	if before != after {
		t.Errorf("rerun changed provisioned files:\n%s\n---\n%s", before, after)
	}
	for _, skipped := range []string{"GitHub CLI (gh) from https://cli.github.com/packages: skipped (already installed",
		"swap file at /swapfile: skipped (/swapfile is already active"} {
		if !strings.Contains(e.out.String(), skipped) {
			t.Errorf("rerun plan does not say %q:\n%s", skipped, e.out.String())
		}
	}
}

// An apt gh that is too old for `gh auth status --json` is upgraded from
// GitHub's repository instead of being kept because it is present.
func TestProvisionUpgradesATooOldGH(t *testing.T) {
	e, f := newProvisionEnv(t)
	writeTestFile(t, e.sys("/usr/bin/gh"), "old gh", 0o755)
	f.oldGH = true
	if err := e.install("--components", "claude", "--switch", "none"); err != nil {
		t.Fatalf("install: %v\n%s", err, e.diag.String())
	}
	if !f.ran("env DEBIAN_FRONTEND=noninteractive apt-get install -y gh") {
		t.Errorf("a too-old gh was not upgraded: %v", f.io)
	}
	if want := "upgrading: the installed gh is too old for codvps): will run"; !strings.Contains(e.out.String(), want) {
		t.Errorf("plan does not say %q:\n%s", want, e.out.String())
	}

	// A current gh that is present is left alone.
	e, f = newProvisionEnv(t)
	writeTestFile(t, e.sys("/usr/bin/gh"), "current gh", 0o755)
	if err := e.install("--components", "claude", "--switch", "none"); err != nil {
		t.Fatalf("install: %v\n%s", err, e.diag.String())
	}
	if f.ran("env DEBIAN_FRONTEND=noninteractive apt-get install -y gh") {
		t.Errorf("a current gh was reinstalled: %v", f.io)
	}
}

// The plan explains every coding CLI: an unselected one is skipped as
// such, a selected one waits for its first enable or login.
func TestProvisionFollowsComponentSelection(t *testing.T) {
	for _, tc := range []struct {
		components string
		claude     string
	}{
		{"none", "skipped (claude is not a selected component)"},
		{"claude", "skipped (installed on first codvps enable claude or login claude)"},
	} {
		e, f := newProvisionEnv(t)
		writeFstab(e)
		if err := e.install("--components", tc.components); err != nil {
			t.Fatalf("%s: %v", tc.components, err)
		}
		if f.ran("claude.ai/install.sh") {
			t.Errorf("%s: install downloaded Claude", tc.components)
		}
		if !strings.Contains(e.out.String(), tc.claude) {
			t.Errorf("%s: plan does not say %q", tc.components, tc.claude)
		}
	}
}

// Provisioning manages the files it writes: a different GitHub source
// list is replaced (and named), an existing profile at codvps's own path is
// kept and activated rather than fetched again, and an inactive /swapfile
// -- which may hold data -- is never reformatted.
func TestProvisionManagesItsFilesButNeverReformatsASwapFile(t *testing.T) {
	e, f := newProvisionEnv(t)
	writeFstab(e)
	writeTestFile(t, e.sys(githubSourcesList), "deb site-mirror\n", 0o644)
	writeTestFile(t, e.sys(BwrapProfile), "site profile\n", 0o644)
	writeTestFile(t, e.sys(swapFile), "data", 0o644)
	if err := e.install("--components", "none"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got := readTestFile(t, e.sys(githubSourcesList)); !strings.HasPrefix(got, "deb [arch=amd64 signed-by=") {
		t.Errorf("github-cli.list was not replaced: %q", got)
	}
	if !strings.Contains(e.out.String(), "replacing "+githubSourcesList+": it differs from what codvps provisions there") {
		t.Errorf("the replaced source list was not named:\n%s", e.out.String())
	}
	if got := readTestFile(t, e.sys(BwrapProfile)); got != "site profile\n" || f.ran("dpkg-deb") {
		t.Errorf("an existing bwrap profile was fetched again: %q", got)
	}
	if got := readTestFile(t, e.sys(swapFile)); got != "data" || f.ran("mkswap") {
		t.Errorf("an inactive /swapfile was reformatted")
	}
	if !strings.Contains(e.diag.String(), "/swapfile exists but is not active swap; leaving it as is") {
		t.Errorf("no warning about the inactive swap file: %s", e.diag.String())
	}
}

// The distro's own bwrap profile is used as is.
func TestProvisionUsesTheDistroBwrapProfile(t *testing.T) {
	e, f := newProvisionEnv(t)
	f.distroBwrp = true
	writeFstab(e)
	writeTestFile(t, e.sys(distroBwrapProfile), "distro\n", 0o644)
	if err := e.install("--components", "none"); err != nil {
		t.Fatal(err)
	}
	if f.ran("sh -c") || fileExistsAt(e.sys(BwrapProfile)) {
		t.Errorf("extracted a profile although the distro ships one")
	}
}

// Kernel-level steps belong to the container host.
func TestProvisionInContainerSkipsKernelSteps(t *testing.T) {
	e, f := newProvisionEnv(t)
	writeFstab(e)
	writeTestFile(t, e.sys("/.dockerenv"), "", 0o644)
	if err := e.install("--components", "none"); err != nil {
		t.Fatal(err)
	}
	for _, never := range []string{"ufw", "fallocate", "swapon", "apparmor_parser", "env DEBIAN_FRONTEND=noninteractive apt-get install -y ufw"} {
		if f.ran(never) {
			t.Errorf("ran %q in a container", never)
		}
	}
	if !fileExistsAt(e.sys(BwrapProfile)) {
		t.Errorf("the profile file should still be provisioned for doctor")
	}
}

// --skip-provision leaves the host as it is.
func TestSkipProvisionRunsNoProvisioning(t *testing.T) {
	e, f := newProvisionEnv(t)
	e.tool("uv")
	if err := e.install("--components", "claude", "--skip-provision"); err != nil {
		t.Fatal(err)
	}
	if len(f.io) != 0 {
		t.Fatalf("provisioning ran: %v", f.io)
	}
	if !strings.Contains(e.out.String(), "host provisioning skipped (--skip-provision)") {
		t.Errorf("skip not reported: %s", e.out.String())
	}
}

// A failed package install stops install before codvps writes anything
// of its own; a failed soft step (swap here) only warns.
func TestProvisionFailureStopsBeforeCodvpsWrites(t *testing.T) {
	e, f := newProvisionEnv(t)
	f.failApt = true
	err := e.install("--components", "claude")
	if err == nil || !strings.Contains(err.Error(), "host provisioning failed (apt packages") {
		t.Fatalf("got %v", err)
	}
	mustAbsent(t, e.sys("/etc/codvps/components.json"))
	mustAbsent(t, e.sys("/usr/local/bin/codvps"))
}
