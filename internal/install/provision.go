package install

// Host provisioning: the network-dependent half of the reference
// installer, run by `sudo codvps install` before it wires codvps up. The
// bootstrap script (install.sh) only fetches and verifies the binary; every
// host change lives here so it goes through the injected runner, is shown
// before it runs, and is unit-tested with fakes.
//
// Each step is skipped when what it provides is already on the host, so a
// rerun changes nothing that is in place, and the vendor CLI steps are
// the selected providers' own (providers.Provision). `--skip-provision`
// skips all of it for a host that is already provisioned.

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/egginsect/codvps/internal/components"
	"github.com/egginsect/codvps/internal/ghcli"
	"github.com/egginsect/codvps/internal/providers"
)

// SkipProvisionFlag is `codvps install`'s opt-out of host provisioning.
const SkipProvisionFlag = "--skip-provision"

// Provisioned system paths and sources.
const (
	// BwrapProfile is the bubblewrap AppArmor profile codvps provisions
	// when the distro does not ship one (doctor checks the same path).
	BwrapProfile       = "/etc/apparmor.d/codvps-bwrap-userns-restrict"
	distroBwrapProfile = "/etc/apparmor.d/bwrap-userns-restrict"
	bwrapProfileInDeb  = "usr/share/apparmor/extra-profiles/bwrap-userns-restrict"

	githubKeyring     = ghcli.Keyring
	githubSourcesList = ghcli.SourcesList
	githubPackagesURL = ghcli.PackagesURL

	nodesourceSetupURL = "https://deb.nodesource.com/setup_lts.x"
	uvInstallerURL     = "https://astral.sh/uv/install.sh"

	// nodePath is where the systemd units find node (not an nvm shim).
	nodePath = "/usr/bin/node"

	swapFile = "/swapfile"
	swapSize = "4G"
	fstab    = "/etc/fstab"
)

// basePackages are the apt packages every codvps host needs: git and ssh
// for repositories, jq/rg/bwrap for the coding CLIs (doctor checks them),
// apparmor for the bubblewrap profile, iproute2 for doctor's socket checks.
var basePackages = []string{
	"git", "openssh-client", "curl", "ca-certificates", "jq", "ripgrep",
	"bubblewrap", "apparmor", "iproute2",
}

// provisionStep is one host provisioning step. skip, evaluated when the
// plan is shown, names why the step is not needed ("" means it runs).
// A soft step's failure is a warning, as in the reference (swap, ufw, uv).
type provisionStep struct {
	title string
	skip  string
	soft  bool
	run   func() error
}

// provisioner runs the steps for one install.
type provisioner struct {
	h         *host
	chosen    *components.Registry
	container bool
}

func (h *host) newProvisioner(chosen *components.Registry) *provisioner {
	return &provisioner{h: h, chosen: chosen, container: h.inContainer()}
}

// inContainer mirrors the reference's is_container: kernel-level policy
// (AppArmor activation, swap, the firewall) belongs to the container host.
func (h *host) inContainer() bool {
	if _, err := os.Lstat(h.Sys("/.dockerenv")); err == nil {
		return true
	}
	return h.Succeeds("systemd-detect-virt", "--container")
}

// systemSteps run as root before codvps writes anything of its own: the
// system packages, then each root-installed vendor CLI, then swap and the
// firewall.
func (p *provisioner) systemSteps() []provisionStep {
	h := p.h
	steps := []provisionStep{
		{title: "apt packages: " + strings.Join(basePackages, " "), run: p.installBasePackages},
		p.bwrapProfileStep(),
		p.githubCLIStep(),
		{title: "Node.js LTS from NodeSource", skip: presentSkip(h.Sys(nodePath)), run: p.installNode},
		{title: "pnpm through corepack", skip: presentSkip(h.Sys("/usr/bin/pnpm")), run: p.enablePNPM},
	}
	steps = append(steps, p.vendorSteps(false)...)
	steps = append(steps,
		provisionStep{title: swapSize + " swap file at " + swapFile, skip: p.swapSkip(), soft: true, run: p.configureSwap},
		provisionStep{title: "ufw firewall (allow SSH, deny other incoming)", skip: p.containerSkip("ufw"), soft: true, run: p.configureUFW},
	)
	return steps
}

// operatorSteps run as the operator once install has created the
// operator's private state (a vendor installer that writes into the
// operator's home needs its directory to be the verified private one).
func (p *provisioner) operatorSteps() []provisionStep {
	h := p.h
	return append(p.vendorSteps(true),
		provisionStep{title: "uv (astral.sh installer into ~/.local/bin)", skip: presentSkip(filepath.Join(h.op.Home, ".local", "bin", "uv")), soft: true, run: p.installUV},
	)
}

// vendorSteps are the providers' vendor installs that run as the operator
// (asOperator) or as root. A provider that is not selected shows as
// skipped, a coding CLI waits for its first enable or login, and one whose
// CLI is already present is not reinstalled.
func (p *provisioner) vendorSteps(asOperator bool) []provisionStep {
	var steps []provisionStep
	for _, prov := range p.h.installed() {
		spec := prov.Provision
		if spec == nil || spec.Run == nil || spec.AsOperator != asOperator {
			continue
		}
		step := provisionStep{title: spec.Title, run: func() error { return spec.Run(p) }}
		switch {
		case !p.chosen.Has(prov):
			step.skip = prov.Name + " is not a selected component"
		case prov.Kind == providers.CodingCLI:
			// Installed on first use: codvps enable/login runs the vendor
			// installer, so install downloads no coding CLI.
			step.skip = "installed on first codvps enable " + prov.Name + " or login " + prov.Name
		default:
			step.skip = p.presentSkip(spec.Present)
		}
		steps = append(steps, step)
	}
	return steps
}

// presentSkip is the first of paths that is already an executable ("~/"
// paths under the operator's home, the rest under the system root).
func (p *provisioner) presentSkip(paths []string) string {
	for _, path := range paths {
		resolved := p.h.Sys(path)
		if rest, ok := strings.CutPrefix(path, "~/"); ok {
			resolved = filepath.Join(p.h.op.Home, rest)
		}
		if s := presentSkip(resolved); s != "" {
			return s
		}
	}
	return ""
}

// Home implements providers.Provisioner.
func (p *provisioner) Home() string { return p.h.op.Home }

// Cmd implements providers.Provisioner.
func (p *provisioner) Cmd(name string, args ...string) error { return p.cmd(name, args...) }

// AsOperator implements providers.Provisioner.
func (p *provisioner) AsOperator(env []string, script string) error { return p.asOperator(env, script) }

// showPlan prints every provisioning step and whether it runs, before any
// of them does.
func (p *provisioner) showPlan(steps []provisionStep) {
	h := p.h
	h.Printf("Host provisioning (%s skips it):\n", SkipProvisionFlag)
	for _, s := range steps {
		if s.skip != "" {
			h.Printf("  - %s: skipped (%s)\n", s.title, s.skip)
		} else {
			h.Printf("  - %s: will run\n", s.title)
		}
	}
	for _, prov := range h.installed() {
		if prov.Provision != nil && prov.Provision.Note != "" && p.chosen.Has(prov) {
			h.Printf("  - %s: %s\n", prov.Provision.Title, prov.Provision.Note)
		}
	}
}

// runSteps runs the steps that are not skipped, in order.
func (p *provisioner) runSteps(steps []provisionStep) error {
	for _, s := range steps {
		if s.skip != "" {
			continue
		}
		p.h.Printf("==> %s\n", s.title)
		if err := s.run(); err != nil {
			if s.soft {
				p.h.Warnf("%s failed: %v; skipping", s.title, err)
				continue
			}
			return fmt.Errorf("host provisioning failed (%s): %w", s.title, err)
		}
	}
	return nil
}

func presentSkip(path string) string {
	if isExecutable(path) {
		return "already installed: " + path
	}
	return ""
}

func (p *provisioner) containerSkip(what string) string {
	if p.container {
		return what + " is unavailable in a container"
	}
	return ""
}

// cmd shows a command, then runs it with its output streamed.
func (p *provisioner) cmd(name string, args ...string) error {
	h := p.h
	h.Printf("+ %s\n", strings.Join(append([]string{name}, args...), " "))
	code, err := h.opts.Runner.RunWithIO(name, args, nil, h.opts.Out, h.opts.Diag)
	if code != 0 || err != nil {
		return fmt.Errorf("%s %s failed: %s", name, strings.Join(args, " "), describe("", code, err))
	}
	return nil
}

// apt runs apt-get non-interactively.
func (p *provisioner) apt(args ...string) error {
	return p.cmd("env", append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get"}, args...)...)
}

// asOperator runs a shell snippet as the operator with a scrubbed
// environment, the way the reference ran the vendor installers.
func (p *provisioner) asOperator(env []string, script string) error {
	h := p.h
	args := []string{"-u", h.op.Name, "--", "env", "-i",
		"HOME=" + h.op.Home, "PATH=/usr/local/bin:/usr/bin:/bin"}
	args = append(args, env...)
	args = append(args, "bash", "-o", "pipefail", "-c", script)
	return p.cmd("runuser", args...)
}

func (p *provisioner) installBasePackages() error {
	if err := p.apt("update"); err != nil {
		return err
	}
	return p.apt(append([]string{"install", "-y"}, basePackages...)...)
}

// bwrapProfileStep provisions the bubblewrap AppArmor profile at its
// codvps-named path. Ubuntu ships it only inside apparmor-profiles, which
// also activates unrelated service policies, so only the one profile is
// extracted from the package.
func (p *provisioner) bwrapProfileStep() provisionStep {
	h := p.h
	step := provisionStep{title: "bubblewrap AppArmor profile at " + BwrapProfile, run: p.installBwrapProfile}
	if fileExistsAt(h.Sys(distroBwrapProfile)) && h.Succeeds("dpkg-query", "-S", distroBwrapProfile) {
		step.skip = "the distro ships " + distroBwrapProfile
	}
	return step
}

func (p *provisioner) installBwrapProfile() error {
	h := p.h
	if !fileExistsAt(h.Sys(BwrapProfile)) {
		tmp, err := os.MkdirTemp("", "codvps-apparmor-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		// apt-get download writes into the working directory.
		if err := p.cmd("sh", "-c", `cd -- "$1" && exec apt-get download -o APT::Sandbox::User=root apparmor-profiles`, "sh", tmp); err != nil {
			return err
		}
		debs, _ := filepath.Glob(filepath.Join(tmp, "apparmor-profiles_*.deb"))
		if len(debs) != 1 {
			return fmt.Errorf("apt-get download did not produce exactly one apparmor-profiles package in %s", tmp)
		}
		extracted := filepath.Join(tmp, "extracted")
		if err := p.cmd("dpkg-deb", "--extract", debs[0], extracted); err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(extracted, bwrapProfileInDeb))
		if err != nil {
			return fmt.Errorf("apparmor-profiles has no %s: %w", bwrapProfileInDeb, err)
		}
		h.Printf("writing %s\n", BwrapProfile)
		if err := writeFile(h.Sys(BwrapProfile), data, 0o644); err != nil {
			return err
		}
	}
	switch {
	case p.container:
		h.Warnf("bubblewrap AppArmor profile activation unavailable in a container; skipping")
	case !h.Succeeds("aa-enabled"):
		h.Warnf("AppArmor is not enabled; skipping bubblewrap profile activation")
	default:
		return p.cmd("apparmor_parser", "-r", h.Sys(BwrapProfile))
	}
	return nil
}

// installGitHubCLI installs (or upgrades) gh through the shared ghcli
// installer, acting as root.
func (p *provisioner) installGitHubCLI() error { return ghcli.Install(ghHost{p}) }

// ghHost is the provisioner as a ghcli.Host.
type ghHost struct{ p *provisioner }

func (g ghHost) Printf(format string, args ...any)      { g.p.h.Printf(format, args...) }
func (g ghHost) Run(name string, args ...string) error  { return g.p.cmd(name, args...) }
func (g ghHost) Root(name string, args ...string) error { return g.p.cmd(name, args...) }
func (g ghHost) Exists(path string) bool                { return fileExistsAt(g.p.h.Sys(path)) }
func (g ghHost) Write(path string, data []byte, mode os.FileMode) error {
	return g.p.writeProvisionedFile(path, data, mode)
}
func (g ghHost) Output(name string, args ...string) (string, error) {
	out, stderr, code, err := g.p.h.opts.Runner.Run(name, args...)
	if code != 0 || err != nil {
		return "", errors.New(describe(stderr, code, err))
	}
	return out, nil
}

// githubCLIStep installs gh, and upgrades one that is too old for codvps
// (`gh auth status --json`, as the distro's package is) instead of keeping it.
func (p *provisioner) githubCLIStep() provisionStep {
	step := provisionStep{title: "GitHub CLI (gh) from " + githubPackagesURL, run: p.installGitHubCLI}
	step.skip = presentSkip(p.h.Sys("/usr/bin/gh"))
	if step.skip == "" {
		return step
	}
	_, stderr, code, err := p.h.opts.Runner.Run("gh", ghcli.ProbeArgs...)
	if ghcli.Classify(stderr, code, err) == ghcli.TooOld {
		step.skip = ""
		step.title += " (upgrading: the installed gh is too old for codvps)"
	}
	return step
}

// writeProvisionedFile writes a provisioning file, replacing (and naming)
// a different one already there: provisioning is the single manager of the
// files it writes.
func (p *provisioner) writeProvisionedFile(path string, data []byte, mode os.FileMode) error {
	h := p.h
	if fileExistsAt(h.Sys(path)) {
		if same, err := sameContent(h.Sys(path), data); err == nil && same {
			return nil
		}
		h.Printf("replacing %s: it differs from what codvps provisions there\n", path)
	} else {
		h.Printf("writing %s\n", path)
	}
	return writeFile(h.Sys(path), data, mode)
}

// installNode uses NodeSource's apt repository rather than nvm: the
// systemd units need node at /usr/bin/node, not behind an nvm shim.
func (p *provisioner) installNode() error {
	h := p.h
	tmp, err := os.MkdirTemp("", "codvps-node-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	setup := filepath.Join(tmp, "setup_lts.x")
	if err := p.cmd("curl", "-fsSL", "--proto", "=https", "-o", setup, nodesourceSetupURL); err != nil {
		return err
	}
	if err := p.cmd("bash", setup); err != nil {
		return err
	}
	if err := p.apt("install", "-y", "nodejs"); err != nil {
		return err
	}
	if !isExecutable(h.Sys(nodePath)) {
		return fmt.Errorf("NodeSource did not install node at %s", nodePath)
	}
	return nil
}

func (p *provisioner) enablePNPM() error {
	if err := p.cmd("corepack", "enable"); err != nil {
		return err
	}
	return p.cmd("corepack", "prepare", "pnpm@latest", "--activate")
}

func (p *provisioner) installUV() error {
	return p.asOperator(nil, "curl -LsSf --proto =https "+uvInstallerURL+" | sh")
}

var fstabSwapLine = regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(swapFile) + `[ \t]`)

func (p *provisioner) swapSkip() string {
	if s := p.containerSkip("swap"); s != "" {
		return s
	}
	if p.swapActive() && p.fstabHasSwap() {
		return swapFile + " is already active and in " + fstab
	}
	return ""
}

func (p *provisioner) swapActive() bool {
	out, _, code, err := p.h.opts.Runner.Run("swapon", "--show=NAME", "--noheadings")
	if code != 0 || err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == swapFile {
			return true
		}
	}
	return false
}

func (p *provisioner) fstabHasSwap() bool {
	data, err := os.ReadFile(p.h.Sys(fstab))
	return err == nil && fstabSwapLine.Match(data)
}

// configureSwap creates and enables /swapfile and makes it permanent. An
// existing /swapfile that is not active swap may hold data, so it is never
// reformatted.
func (p *provisioner) configureSwap() error {
	h := p.h
	if !p.swapActive() {
		if fileExistsAt(h.Sys(swapFile)) {
			return fmt.Errorf("%s exists but is not active swap; leaving it as is (enable or remove it, then rerun)", swapFile)
		}
		if err := p.cmd("fallocate", "-l", swapSize, h.Sys(swapFile)); err != nil {
			return err
		}
		if err := os.Chmod(h.Sys(swapFile), 0o600); err != nil {
			return fmt.Errorf("failed to set the mode of %s: %w", swapFile, err)
		}
		if err := p.cmd("mkswap", h.Sys(swapFile)); err != nil {
			return err
		}
		if err := p.cmd("swapon", h.Sys(swapFile)); err != nil {
			return err
		}
	}
	if p.fstabHasSwap() {
		return nil
	}
	h.Printf("adding %s to %s\n", swapFile, fstab)
	f, err := os.OpenFile(h.Sys(fstab), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", fstab, err)
	}
	if _, err := fmt.Fprintf(f, "%s none swap sw 0 0\n", swapFile); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to update %s: %w", fstab, err)
	}
	return f.Close()
}

// configureUFW allows SSH on every port sshd listens on before denying
// other incoming traffic, so enabling the firewall never locks the
// operator out.
func (p *provisioner) configureUFW() error {
	if err := p.apt("install", "-y", "ufw"); err != nil {
		return err
	}
	if err := p.cmd("ufw", "allow", "OpenSSH"); err != nil {
		return err
	}
	for _, port := range p.sshPorts() {
		if err := p.cmd("ufw", "allow", port); err != nil {
			return err
		}
	}
	if err := p.cmd("ufw", "default", "deny", "incoming"); err != nil {
		return err
	}
	return p.cmd("ufw", "--force", "enable")
}

var digits = regexp.MustCompile(`^[0-9]+$`)

// sshPorts asks sshd for its effective ports, falls back to
// sshd_config's Port lines, and defaults to 22.
func (p *provisioner) sshPorts() []string {
	var ports []string
	seen := map[string]bool{}
	add := func(port string) {
		if digits.MatchString(port) && !seen[port] {
			seen[port] = true
			ports = append(ports, port)
		}
	}
	if out, _, code, err := p.h.opts.Runner.Run("sshd", "-T"); code == 0 && err == nil {
		for _, line := range strings.Split(out, "\n") {
			if f := strings.Fields(line); len(f) == 2 && f[0] == "port" {
				add(f[1])
			}
		}
	}
	if len(ports) == 0 {
		if f, err := os.Open(p.h.Sys("/etc/ssh/sshd_config")); err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				if fields := strings.Fields(sc.Text()); len(fields) >= 2 && fields[0] == "Port" {
					add(fields[1])
				}
			}
			_ = f.Close()
		}
	}
	if len(ports) == 0 {
		ports = []string{"22"}
	}
	return ports
}

func fileExistsAt(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// provisionSystem shows the whole provisioning plan, then runs the root
// steps. It is a no-op when provisioning is disabled.
func (h *host) provisionSystem(p *provisioner) error {
	if p == nil {
		return nil
	}
	sys := p.systemSteps()
	p.showPlan(append(append([]provisionStep{}, sys...), p.operatorSteps()...))
	return p.runSteps(sys)
}

// provisionOperator runs the operator steps (vendor installers into the
// operator's home). It is a no-op when provisioning is disabled.
func (h *host) provisionOperator(p *provisioner) error {
	if p == nil {
		return nil
	}
	return p.runSteps(p.operatorSteps())
}

// provisionerFor returns the provisioner for this install, or nil when
// provisioning is off (Options.Provision unset, or --skip-provision).
func (h *host) provisionerFor(chosen *components.Registry, skip bool) *provisioner {
	if !h.opts.Provision || skip {
		if h.opts.Provision {
			h.Printf("host provisioning skipped (%s)\n", SkipProvisionFlag)
		}
		return nil
	}
	return h.newProvisioner(chosen)
}
