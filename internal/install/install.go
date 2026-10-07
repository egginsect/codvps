// Package install implements `codvps install` and `codvps uninstall`:
// the root-run steps that turn a host with the codvps binary and
// the vendor CLIs on disk into a working codvps host, and back.
//
// Behavior is re-authored from the reference implementation's install.sh
// and uninstall.sh contracts. What is installed per coding CLI -- unit
// files, generated drop-ins, PATH links, the vendor CLI, the operator's
// private state, and the head's upgrade hooks -- is declared by that CLI's
// definition in internal/providers; this package runs those declarations
// for every provider, in order:
//
//   - refuse bare root and a root SUDO_USER (systemd --user and linger need
//     a login account), and whatever a provider's preflight refuses (a
//     symlinked legacy Codex workspace root or Codex Remote home);
//   - resolve the component selection (flags, the existing registry,
//     detection, or an interactive prompt) and write the root-owned
//     registry;
//   - install the binary at /usr/local/bin/codvps and every declared unit
//     file. install is the single manager of these files: an existing file
//     of the same name is replaced (and named), never refused;
//   - create the operator's private state (as the operator, never as root);
//   - enable linger, run each provider's upgrade hooks around the unit
//     rewrite, and link the operator's uv, uvx and the selected providers'
//     executables onto the systemd unit PATH.
//
// The managed runtimes are wired up here too: the updater and
// publisher units and their /run drop boxes, a seeded release and
// selection per selected provider (see legacy.go for the retired pins).
package install

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egginsect/codvps/internal/components"
	"github.com/egginsect/codvps/internal/fsutil"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/runner"
)

// Usage is `codvps install`'s usage line for the given providers.
func Usage(set providers.Set) string {
	return "codvps install [--components " + strings.Join(set.Implemented(providers.CodingCLI).Names(), ",") +
		"|none] [--switch " + strings.Join(append([]string{"none"}, set.Implemented(providers.Switch).Names()...), "|") +
		"] [" + SkipProvisionFlag + "]"
}

// errNonRootOperator is the reference's refusal of bare root or
// SUDO_USER=root, adapted only by product name.
const errNonRootOperator = "run the installer as a normal sudo-capable user via sudo, because systemd --user + linger need a login account"

// localBin is the /usr/local/bin directory install links tooling into.
const localBin = "/usr/local/bin"

// coreLinks are linked into localBin on every install: the units run uv
// and uvx from the operator's ~/.local/bin. A missing one is a warning.
var coreLinks = []string{"uv", "uvx"}

// pathLine is the ~/.bashrc line that keeps ~/.local/bin (uv, uvx, the
// linked vendor CLIs) on interactive and login shells' PATH.
const pathLine = `export PATH="$HOME/.local/bin:$PATH"`

// Operator is the login account install and uninstall act for.
type Operator = providers.Operator

// OperatorStateFunc creates the operator's private state as the operator
// (production runs `codvps internal operator-state <provider>...` through
// runuser) for the named selected providers; see EnsureOperatorState.
type OperatorStateFunc func(op Operator, namespace string, providerNames []string) error

// Options are install's and uninstall's injected dependencies.
type Options struct {
	// Root prefixes every system path (/etc, /usr/local, /opt). It is ""
	// in production and a temporary directory in tests; it never comes
	// from the environment.
	Root string
	// Namespace is the codvps namespace (paths.DefaultNamespace unless
	// CODVPS_NAMESPACE says otherwise).
	Namespace string
	// EUID is the caller's effective uid; install and uninstall require 0.
	EUID int
	// SudoUser is $SUDO_USER, the account that ran sudo.
	SudoUser string
	// Ident resolves the operator's home and uid/gid.
	Ident repo.IdentityLookup
	// Runner runs loginctl, systemctl, and runuser (for the operator's
	// systemd --user manager).
	Runner runner.Runner
	// OperatorState creates the operator-owned state as the operator.
	OperatorState OperatorStateFunc
	// AsOperator runs `codvps internal <args...>` as the operator, so root
	// never writes into a directory they control.
	AsOperator func(op Operator, namespace string, args ...string) error
	// ProcRoot is where running processes are inspected ("/proc" when
	// empty): the retired runtime tree is kept while one still executes
	// from it.
	ProcRoot string
	// Executable is the running codvps binary, copied to
	// /usr/local/bin/codvps.
	Executable func() (string, error)
	// Stdin and IsTTY drive the component-selection prompt on a fresh
	// host with a terminal.
	Stdin io.Reader
	IsTTY bool
	// Out receives progress; Diag receives warnings.
	Out  io.Writer
	Diag io.Writer
	// Providers are the coding CLIs and switches install can select and
	// wire up (providers.All() in production).
	Providers providers.Set
	// Provision runs host provisioning (provision.go) through
	// Runner: true in production, false in tests that exercise only the
	// codvps wiring.
	Provision bool
}

func (o *Options) validate() error {
	switch {
	case o.Ident == nil:
		return errors.New("install: Ident is required")
	case o.Runner == nil:
		return errors.New("install: Runner is required")
	case o.OperatorState == nil:
		return errors.New("install: OperatorState is required")
	case o.AsOperator == nil:
		return errors.New("install: AsOperator is required")
	case o.Executable == nil:
		return errors.New("install: Executable is required")
	case o.Out == nil || o.Diag == nil:
		return errors.New("install: Out and Diag writers are required")
	case len(o.Providers) == 0:
		return errors.New("install: Providers are required")
	}
	return o.Providers.Validate()
}

// host is one resolved install/uninstall run. It is the
// providers.InstallHost every provider hook acts through.
type host struct {
	opts   Options
	op     Operator
	layout *paths.Layout
}

// installed are the providers with an install facet, in registry order.
func (h *host) installed() providers.Set {
	return h.opts.Providers.Where(func(p *providers.Provider) bool { return p.Planned == "" && p.Install != nil })
}

// catalog is the component registry's view of the providers.
func (h *host) catalog() components.Catalog {
	return components.Catalog{Providers: h.opts.Providers}
}

// Operator implements providers.InstallHost.
func (h *host) Operator() Operator { return h.op }

// Layout implements providers.InstallHost.
func (h *host) Layout() *paths.Layout { return h.layout }

// Sys resolves a production system path under the test root.
func (h *host) Sys(path string) string { return filepath.Join(h.opts.Root, path) }

// Printf implements providers.InstallHost.
func (h *host) Printf(format string, args ...any) {
	_, _ = fmt.Fprintf(h.opts.Out, format, args...)
}

// Warnf implements providers.InstallHost.
func (h *host) Warnf(format string, args ...any) {
	_, _ = fmt.Fprintf(h.opts.Diag, "WARN: "+format+"\n", args...)
}

// resolveHost applies the checks shared by install and uninstall: root,
// a non-root sudo operator, the default namespace, and the operator's
// identity.
func resolveHost(opts Options, command string) (*host, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if opts.EUID != 0 {
		return nil, fmt.Errorf("codvps %s must be run as root: sudo codvps %s", command, command)
	}
	if opts.SudoUser == "" || opts.SudoUser == "root" {
		if command == "uninstall" {
			return nil, errors.New("run the uninstaller as a normal sudo-capable user via sudo, because systemd --user units belong to a login account")
		}
		return nil, errors.New(errNonRootOperator)
	}
	ns := opts.Namespace
	if ns == "" {
		ns = paths.DefaultNamespace
	}
	// Unit names are not namespace-prefixed yet, so a side-by-side install
	// under another namespace would write and stop the production units.
	if ns != paths.DefaultNamespace {
		return nil, fmt.Errorf("codvps %s supports only the default %q namespace: unit names are not namespace-prefixed yet, so CODVPS_NAMESPACE=%s would collide with a production install", command, paths.DefaultNamespace, ns)
	}
	home, err := opts.Ident.OperatorHome(opts.SudoUser)
	if err != nil || home == "" {
		return nil, fmt.Errorf("could not resolve a home directory for operator: %s", opts.SudoUser)
	}
	uid, gid, err := opts.Ident.OperatorIDs(opts.SudoUser)
	if err != nil {
		return nil, fmt.Errorf("could not resolve a uid/gid for operator %s: %w", opts.SudoUser, err)
	}
	layout, err := paths.NewForHome(home, ns)
	if err != nil {
		return nil, err
	}
	return &host{
		opts:   opts,
		op:     Operator{Name: opts.SudoUser, Home: home, UID: uid, GID: gid},
		layout: layout.WithSystemRoot(opts.Root),
	}, nil
}

// selection is the parsed --components/--switch flags.
type selection struct {
	components    string
	switchVal     string
	hasComponents bool
	hasSwitch     bool
	skipProvision bool
}

func parseInstallArgs(set providers.Set, args []string) (selection, error) {
	var s selection
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--components":
			if i+1 >= len(args) {
				return s, fmt.Errorf("--components requires a value, e.g. --components %s", strings.Join(set.Implemented(providers.CodingCLI).Names(), ","))
			}
			i++
			s.components, s.hasComponents = args[i], true
		case strings.HasPrefix(arg, "--components="):
			s.components, s.hasComponents = strings.TrimPrefix(arg, "--components="), true
		case arg == "--switch":
			if i+1 >= len(args) {
				return s, fmt.Errorf("--switch requires a value: %s", strings.Join(append([]string{"none"}, set.Implemented(providers.Switch).Names()...), " or "))
			}
			i++
			s.switchVal, s.hasSwitch = args[i], true
		case strings.HasPrefix(arg, "--switch="):
			s.switchVal, s.hasSwitch = strings.TrimPrefix(arg, "--switch="), true
		case arg == SkipProvisionFlag:
			s.skipProvision = true
		default:
			return s, fmt.Errorf("unknown install argument: %s (usage: %s)", arg, Usage(set))
		}
	}
	return s, nil
}

// Install runs `codvps install`. Every refusal happens before the first
// host mutation; after that, each step is idempotent, so rerunning install
// repairs a partially installed host.
func Install(opts Options, args []string) error {
	h, err := resolveHost(opts, "install")
	if err != nil {
		return err
	}
	for _, p := range h.installed() {
		if p.Install.Preflight == nil {
			continue
		}
		if err := p.Install.Preflight(h); err != nil {
			return err
		}
	}
	flags, err := parseInstallArgs(h.opts.Providers, args)
	if err != nil {
		return err
	}
	before := h.installedBefore()
	needs, err := components.FreshHostNeedsSelection(h.layout, before, flags.hasComponents, flags.hasSwitch, h.opts.IsTTY)
	if err != nil {
		return fmt.Errorf("failed to check the component registry: %w", err)
	}
	if needs {
		return errors.New(h.catalog().FreshHostSelectionMessage())
	}
	chosen, report, err := h.resolveSelection(flags, before)
	if err != nil {
		return err
	}
	plan, err := h.planArtifacts()
	if err != nil {
		return err
	}
	selected := h.installed().Where(chosen.Has)

	// Mutations start here.
	prov := h.provisionerFor(chosen, flags.skipProvision)
	if err := h.provisionSystem(prov); err != nil {
		return err
	}
	if err := h.writeRegistry(chosen); err != nil {
		return err
	}
	report()
	if err := h.installBinary(plan.binary); err != nil {
		return err
	}
	if err := h.ensureOperatorState(selected); err != nil {
		return err
	}
	if err := h.provisionOperator(prov); err != nil {
		return err
	}
	if err := h.enableLinger(); err != nil {
		return err
	}
	finish := map[string]providers.Finish{}
	for _, p := range h.installed() {
		if p.Install.Prepare == nil {
			continue
		}
		f, err := p.Install.Prepare(h)
		if err != nil {
			return err
		}
		finish[p.Name] = f
	}
	if err := h.retireLegacyRuntimes(); err != nil {
		return err
	}
	if err := h.retireStaleDropIns(); err != nil {
		return err
	}
	if err := h.installUnits(plan.units); err != nil {
		return err
	}
	if err := h.daemonReload(); err != nil {
		return err
	}
	for _, p := range selected {
		if p.Install.Configure == nil {
			continue
		}
		if err := p.Install.Configure(h); err != nil {
			return err
		}
	}
	if err := h.linkTooling(selected); err != nil {
		return err
	}
	for _, p := range h.installed() {
		if f := finish[p.Name]; f != nil {
			if err := f(h, chosen.Has(p)); err != nil {
				return err
			}
		}
	}
	h.printNextSteps()
	return nil
}

// installedBefore reports whether codvps was installed on this host
// before: any unit file it declares is already in place. (The binary
// itself is no evidence: a bootstrap may have put it there first.)
func (h *host) installedBefore() bool {
	for _, u := range h.productUnits() {
		if _, err := os.Lstat(h.Sys(u.InstallPath)); err == nil {
			return true
		}
	}
	return false
}

// resolveSelection decides the component selection without writing it:
// explicit flags win (a single flag keeps the other axis as recorded),
// then an existing registry, then detection on a host codvps already
// installed, then an interactive prompt. It returns the chosen registry
// and a reporter that prints what was decided once it has been written.
func (h *host) resolveSelection(flags selection, installedBefore bool) (*components.Registry, func(), error) {
	path := components.RegistryPath(h.layout)
	_, statErr := os.Stat(path)
	hasRegistry := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, nil, fmt.Errorf("failed to check the component registry: %w", statErr)
	}
	current := components.DefaultRegistry()
	if hasRegistry {
		reg, err := h.catalog().Read(path)
		if err != nil {
			return nil, nil, err
		}
		current = reg
	}

	switch {
	case flags.hasComponents || flags.hasSwitch:
		clis := flags.components
		if !flags.hasComponents {
			clis = strings.Join(current.Selected(), ",")
		}
		sw := flags.switchVal
		if !flags.hasSwitch {
			sw = current.SwitchValue()
		}
		chosen, err := h.validatedRegistry(clis, sw)
		if err != nil {
			return nil, nil, err
		}
		return chosen, func() {
			h.reportSelection("component selection", chosen)
			h.reportDeselection(current, chosen)
		}, nil
	case hasRegistry:
		return current, func() {
			h.Printf("using existing component selection from %s: coding CLIs=%s switch=%s\n",
				filepath.Join("/etc", h.layout.Namespace(), components.RegistryFileName), csvOrNone(current.Selected()), current.SwitchValue())
		}, nil
	case installedBefore:
		chosen := h.detectExisting()
		return chosen, func() {
			h.Printf("detected an existing installation with no component registry; selected coding CLIs=%s switch=%s\n",
				csvOrNone(chosen.Selected()), chosen.SwitchValue())
		}, nil
	case h.opts.IsTTY:
		chosen, err := h.promptSelection()
		if err != nil {
			return nil, nil, err
		}
		return chosen, func() { h.reportSelection("component selection", chosen) }, nil
	default:
		return nil, nil, errors.New(h.catalog().FreshHostSelectionMessage())
	}
}

func (h *host) validatedRegistry(clisCSV, switchVal string) (*components.Registry, error) {
	clis, err := h.catalog().ValidateCLIs(clisCSV)
	if err != nil {
		return nil, err
	}
	sw, err := h.catalog().ValidateSwitch(switchVal)
	if err != nil {
		return nil, err
	}
	reg := components.DefaultRegistry()
	if clis != "" {
		reg.CodingCLIs = strings.Split(clis, ",")
	}
	reg.Switch = sw
	return reg, nil
}

// detectExisting is the reference's components_detect_existing: infer the
// selection from what an earlier install left on disk (each provider's
// Detect), so a routine reinstall never silently adds or drops a coding
// CLI.
func (h *host) detectExisting() *components.Registry {
	reg := components.DefaultRegistry()
	for _, p := range h.installed() {
		if p.Install.Detect == nil || !p.Install.Detect(h) {
			continue
		}
		if p.Kind == providers.Switch {
			reg.Switch = p.Name
		} else {
			reg.CodingCLIs = append(reg.CodingCLIs, p.Name)
		}
	}
	return reg
}

func (h *host) promptSelection() (*components.Registry, error) {
	if h.opts.Stdin == nil {
		return nil, errors.New(h.catalog().FreshHostSelectionMessage())
	}
	cat := h.catalog()
	allCLIs := strings.Join(cat.KnownCLIs(), ",")
	in := bufio.NewReader(h.opts.Stdin)
	_, _ = fmt.Fprintln(h.opts.Diag, "No component selection found and no --components/--switch flags given.")
	_, _ = fmt.Fprintf(h.opts.Diag, "Coding CLIs to install [%s] (comma list, or none): ", allCLIs)
	clis, err := readLine(in)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(h.opts.Diag, "Switch to install [%s] (default none): ", strings.Join(cat.KnownSwitches(), "/"))
	sw, err := readLine(in)
	if err != nil {
		return nil, err
	}
	if clis == "" {
		clis = allCLIs
	}
	if sw == "" {
		sw = "none"
	}
	return h.validatedRegistry(clis, sw)
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("failed to read the component selection: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func (h *host) reportSelection(label string, reg *components.Registry) {
	h.Printf("%s: coding CLIs=%s switch=%s\n", label, csvOrNone(reg.Selected()), reg.SwitchValue())
}

// reportDeselection names what a deselection leaves behind: deselecting
// never removes anything.
func (h *host) reportDeselection(before, after *components.Registry) {
	for _, p := range h.installed() {
		if !before.Has(p) || after.Has(p) {
			continue
		}
		if p.Kind == providers.Switch {
			h.Printf("note: the %s switch was deselected; nothing was removed.\n", p.Name)
		} else {
			h.Printf("note: %s was deselected; nothing was removed. Its state and credentials stay on disk.\n", p.Name)
		}
	}
}

func csvOrNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ",")
}

// writeRegistry writes /etc/<namespace>/components.json, root-owned 0644
// under a 0755 directory so every operator command can read it.
func (h *host) writeRegistry(reg *components.Registry) error {
	etc := h.layout.EtcPath()
	if err := os.MkdirAll(etc, 0o755); err != nil {
		return fmt.Errorf("failed to create %s: %w", etc, err)
	}
	if err := os.Chmod(etc, 0o755); err != nil {
		return fmt.Errorf("failed to set the mode of %s: %w", etc, err)
	}
	if err := components.Write(components.RegistryPath(h.layout), reg); err != nil {
		return fmt.Errorf("failed to write the component registry: %w", err)
	}
	return nil
}

// ensureOperatorState creates the operator's private state as the
// operator, then verifies it as root before any service can use it.
func (h *host) ensureOperatorState(selected providers.Set) error {
	var names []string
	for _, p := range selected {
		if p.Install.OperatorState != nil {
			names = append(names, p.Name)
		}
	}
	if err := h.opts.OperatorState(h.op, h.layout.Namespace(), names); err != nil {
		return err
	}
	if err := h.VerifyOwned(h.layout.ConfigDir(), true, 0o700); err != nil {
		return fmt.Errorf("codvps state root failed final ownership or mode verification: %w", err)
	}
	for _, p := range selected {
		if p.Install.VerifyOperatorState == nil {
			continue
		}
		if err := p.Install.VerifyOperatorState(h); err != nil {
			return err
		}
	}
	return nil
}

// VerifyOwned requires path to be a real directory or regular file owned
// by the operator's uid:gid with exactly mode.
func (h *host) VerifyOwned(path string, dir bool, mode os.FileMode) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || (dir && !fi.IsDir()) || (!dir && !fi.Mode().IsRegular()) {
		return fmt.Errorf("%s is not a real %s", path, map[bool]string{true: "directory", false: "file"}[dir])
	}
	uid, gid, ok := fsutil.OwnerIDs(fi)
	if !ok || uid != h.op.UID || gid != h.op.GID || fi.Mode().Perm() != mode {
		return fmt.Errorf("%s is %d:%d:%o, want %d:%d:%o", path, uid, gid, fi.Mode().Perm(), h.op.UID, h.op.GID, mode)
	}
	return nil
}

// enableLinger keeps the operator's systemd --user manager (and so the
// Claude heads and the Codex watchdog) running without a login session.
func (h *host) enableLinger() error {
	if err := h.Run("loginctl", "enable-linger", h.op.Name); err != nil {
		return err
	}
	return h.Run("systemctl", "start", "user@"+strconv.Itoa(h.op.UID)+".service")
}

func (h *host) daemonReload() error {
	if err := h.RunUser("daemon-reload"); err != nil {
		return err
	}
	return h.Run("systemctl", "daemon-reload")
}

// printNextSteps names the first commands to run: sign in to every coding
// CLI with a login and enable every head.
func (h *host) printNextSteps() {
	var b strings.Builder
	b.WriteString("\ncodvps installation complete.\n\nNext steps (run codvps login github first to configure SSH and Git):\n  codvps login github\n")

	// Read the registry to determine selected providers
	reg, _ := h.catalog().ReadAt(h.layout)

	for _, p := range h.installed() {
		if p.Credential != nil {
			fmt.Fprintf(&b, "  codvps login %s\n", p.Name)
		}
	}
	for _, p := range h.installed() {
		// Print enable lines for selected providers that have a Head facet
		if p.Head != nil && (reg == nil || reg.Has(p)) {
			fmt.Fprintf(&b, "  codvps enable %s\n", p.Name)
		}
	}
	b.WriteString("  codvps repo add <url>\n  codvps status && codvps doctor\n")
	h.Printf("%s", b.String())
}

// Run runs a root command and fails with its stderr.
func (h *host) Run(name string, args ...string) error {
	_, stderr, code, err := h.opts.Runner.Run(name, args...)
	if code != 0 || err != nil {
		return fmt.Errorf("%s %s failed: %s", name, strings.Join(args, " "), describe(stderr, code, err))
	}
	return nil
}

// Succeeds reports whether a root command exits 0.
func (h *host) Succeeds(name string, args ...string) bool {
	_, _, code, err := h.opts.Runner.Run(name, args...)
	return code == 0 && err == nil
}

// Output is a read-only root command's stdout. A failed probe reads as
// empty: callers report "not present", and the state-changing calls that
// follow surface real failures.
func (h *host) Output(name string, args ...string) string {
	out, _, _, _ := h.opts.Runner.Run(name, args...)
	return out
}

// userArgs addresses the operator's systemd --user manager from root:
// runuser drops to the operator and XDG_RUNTIME_DIR names its bus, which a
// sudo session does not inherit.
func (h *host) userArgs(args ...string) []string {
	return append([]string{"-u", h.op.Name, "--", "env", "XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(h.op.UID), "systemctl", "--user"}, args...)
}

// RunUser runs `systemctl --user <args>` in the operator's manager.
func (h *host) RunUser(args ...string) error {
	return h.Run("runuser", h.userArgs(args...)...)
}

// UserSucceeds reports whether `systemctl --user <args>` exits 0.
func (h *host) UserSucceeds(args ...string) bool {
	return h.Succeeds("runuser", h.userArgs(args...)...)
}

// UserOutput is a read-only listing from the operator's manager. A failed
// listing (no user bus, no such units) reads as empty: the caller then
// reports "not present", and the state-changing calls that follow surface
// real manager failures.
func (h *host) UserOutput(args ...string) string {
	return h.Output("runuser", h.userArgs(args...)...)
}

func describe(stderr string, code int, err error) string {
	if msg := strings.TrimSpace(stderr); msg != "" {
		return msg
	}
	if err != nil {
		return fmt.Sprintf("exit %d: %v", code, err)
	}
	return fmt.Sprintf("exit %d", code)
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// sameContent reports whether path holds exactly want.
func sameContent(path string, want []byte) (bool, error) {
	got, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return bytes.Equal(got, want), nil
}

// writeReader atomically writes an executable root-owned file from r.
func writeReader(path string, r io.Reader) error {
	if err := fsutil.AtomicWriteReader(path, r, 0o755); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

// writeFile atomically writes a root-owned file, creating its directory.
func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create %s: %w", filepath.Dir(path), err)
	}
	if err := fsutil.AtomicWrite(path, data, mode); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}
