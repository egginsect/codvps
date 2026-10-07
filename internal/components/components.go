// Package components manages the codvps component-selection registry: which
// coding CLIs and which switch an install chose (the providers themselves,
// and what each supports, are defined once in internal/providers), and the
// computed status shown by `codvps components status`.
//
// Behavior is re-authored from the reference implementation's component
// registry contract: a root-owned, world-readable JSON file at
// /etc/<namespace>/components.json in the exact shape
// {"version":1,"coding_clis":["claude","codex"],"switch":"none"}.
package components

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/egginsect/codvps/internal/fsutil"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/runner"
)

// InstallCommand is the command this package's gating messages name as
// "where to rerun --components/--switch": `codvps install`, the
// root-run install step of the single codvps binary. The reference named
// its own install.sh from $SCRIPT_DIR; codvps ships no script directory,
// so the messages name the installed command instead, built through this
// one constant so the wording only has to change in one place.
const InstallCommand = "codvps install"

// RegistryFileName is the file name of the component-selection registry
// inside the etc directory (paths.Layout.EtcPath()).
const RegistryFileName = "components.json"

// RegistryVersion is the only schema version this package accepts, matching
// the reference's hard-coded `version: 1`.
const RegistryVersion = 1

// Catalog is the component registry's view of the providers: which names
// --components and --switch accept, how each one is selected, and what
// `codvps components status` reports for it. Production uses
// Catalog{Providers: providers.All()}; tests may add their own providers.
type Catalog struct {
	Providers providers.Set
}

// KnownCLIs are the coding CLIs `--components` and the registry's
// coding_clis array may name (the reference's COMPONENTS_KNOWN_CLIS).
func (c Catalog) KnownCLIs() []string {
	return c.Providers.Implemented(providers.CodingCLI).Names()
}

// KnownSwitches are the values `--switch` and the registry's switch field
// may hold (the reference's COMPONENTS_KNOWN_SWITCHES): "none" first.
func (c Catalog) KnownSwitches() []string {
	return append([]string{"none"}, c.Providers.Implemented(providers.Switch).Names()...)
}

// DescribeCapability returns a provider's capability status, or "unknown"
// when either the component or the action is not recognized (the
// reference's `*) echo unknown`).
func (c Catalog) DescribeCapability(component, action string) string {
	p := c.Providers.Lookup(component)
	if p == nil {
		return "unknown"
	}
	status, ok := p.Capabilities.Get(action)
	if !ok {
		return "unknown"
	}
	return string(status)
}

// Registry holds the on-disk component-selection state, in exactly the
// reference's schema: {"version":1,"coding_clis":[...],"switch":"..."}.
type Registry struct {
	Version    int      `json:"version"`
	CodingCLIs []string `json:"coding_clis"`
	Switch     string   `json:"switch"`
}

// DefaultRegistry is "nothing selected yet": the reference's
// components_default_json, used when no registry file exists.
func DefaultRegistry() *Registry {
	return &Registry{Version: RegistryVersion, CodingCLIs: []string{}, Switch: "none"}
}

// RegistryPath returns the registry file path under layout's etc directory.
// Tests must build layout with paths.Layout.WithSystemRoot(t.TempDir()) so
// this never resolves under the real /etc.
func RegistryPath(layout *paths.Layout) string {
	return filepath.Join(layout.EtcPath(), RegistryFileName)
}

// Read reads and validates the registry at path. A missing file returns
// DefaultRegistry (nothing selected), not an error -- the reference's
// components_read_json falls back to components_default_json rather than
// dying when the file is simply absent. A present-but-invalid file is a
// loud failure: the reference's corrupt check is
// `.version == 1 and (.coding_clis | type) == "array" and (.switch | type)
// == "string"`; this port additionally rejects a coding_clis entry that
// names an unknown CLI or a switch value that is not "", "none", or a
// known switch, so a hand-edited registry can never silently select
// something codvps does not recognize.
func (c Catalog) Read(path string) (*Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultRegistry(), nil
		}
		return nil, fmt.Errorf("failed to read component registry: %w", err)
	}

	var reg Registry
	if err := json.Unmarshal(raw, &reg); err != nil {
		return nil, fmt.Errorf("corrupt component registry: %s: %w", path, err)
	}
	if err := c.validateRegistry(&reg); err != nil {
		return nil, fmt.Errorf("corrupt component registry: %s: %w", path, err)
	}
	return &reg, nil
}

// ReadAt reads the registry at layout's etc directory.
func (c Catalog) ReadAt(layout *paths.Layout) (*Registry, error) {
	return c.Read(RegistryPath(layout))
}

func (c Catalog) validateRegistry(reg *Registry) error {
	if reg.Version != RegistryVersion {
		return fmt.Errorf("version must be %d, got %d", RegistryVersion, reg.Version)
	}
	if reg.CodingCLIs == nil {
		return errors.New("coding_clis must be an array")
	}
	for _, cli := range reg.CodingCLIs {
		if !stringIn(c.KnownCLIs(), cli) {
			return fmt.Errorf("unknown coding CLI in registry: %q", cli)
		}
	}
	if reg.Switch != "" && !stringIn(c.KnownSwitches(), reg.Switch) {
		return fmt.Errorf("unknown switch in registry: %q", reg.Switch)
	}
	return nil
}

// Write writes the registry atomically at 0644 (world-readable, matching
// the reference's root:root 0644 file; this port cannot chown to root
// unprivileged, so ownership is left to whatever calls Write as root, the
// same as the reference's own best-effort `chown 0:0 ... || true`).
func Write(path string, reg *Registry) error {
	if reg.Version == 0 {
		reg.Version = RegistryVersion
	}
	if reg.CodingCLIs == nil {
		reg.CodingCLIs = []string{}
	}
	if reg.Switch == "" {
		reg.Switch = "none"
	}
	data, err := json.Marshal(reg)
	if err != nil {
		return fmt.Errorf("failed to marshal component registry: %w", err)
	}
	data = append(data, '\n')
	return fsutil.AtomicWrite(path, data, 0o644)
}

// IsSelected reports whether name is one of the registry's coding_clis.
func (r *Registry) IsSelected(name string) bool {
	return stringIn(r.CodingCLIs, name)
}

// Selected returns a sorted copy of the registry's coding_clis, with blanks
// and duplicates removed.
func (r *Registry) Selected() []string {
	seen := make(map[string]bool)
	var out []string
	for _, cli := range r.CodingCLIs {
		cli = strings.TrimSpace(cli)
		if cli == "" || seen[cli] {
			continue
		}
		seen[cli] = true
		out = append(out, cli)
	}
	sort.Strings(out)
	return out
}

// SwitchValue returns the registry's switch, defaulting to "none" (the
// reference's `.switch // "none"`).
func (r *Registry) SwitchValue() string {
	if r.Switch == "" {
		return "none"
	}
	return r.Switch
}

// switchSelected reports whether name is the registry's active switch (the
// reference's switch_selected).
func (r *Registry) switchSelected(name string) bool {
	return r.SwitchValue() == name
}

// Has reports whether p is selected: a coding CLI by the coding_clis
// array, a switch by the switch value (the reference's
// runtime_provider_selected). A planned provider is never selected.
func (r *Registry) Has(p *providers.Provider) bool {
	switch {
	case p.Planned != "":
		return false
	case p.Kind == providers.Switch:
		return r.switchSelected(p.Name)
	default:
		return r.IsSelected(p.Name)
	}
}

func stringIn(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// exampleCLIs is the --components value the guidance messages suggest:
// every known coding CLI.
func (c Catalog) exampleCLIs() string {
	return strings.Join(c.KnownCLIs(), ",")
}

// exampleSwitch is the --switch value the guidance messages suggest: the
// first implemented switch.

// ValidateCLIs validates a comma-separated --components value (or
// "none"/empty) against the known coding CLIs, mirroring the reference's
// components_validate_clis_csv. It returns the validated, deduplicated,
// order-preserving CSV on success.
func (c Catalog) ValidateCLIs(csv string) (string, error) {
	var out []string
	seen := make(map[string]bool)
	known := c.KnownCLIs()

	if csv != "" && csv != "none" {
		for _, part := range strings.Split(csv, ",") {
			cli := strings.TrimSpace(part)
			if cli == "" {
				continue
			}
			if !stringIn(known, cli) {
				return "", fmt.Errorf("unknown coding CLI in --components: %s (known: %s)", cli, strings.Join(known, " "))
			}
			if !seen[cli] {
				seen[cli] = true
				out = append(out, cli)
			}
		}
	}
	return strings.Join(out, ","), nil
}

// ValidateSwitch validates a single --switch value, mirroring the
// reference's components_validate_switch. It returns the validated value
// ("none" for an empty input) on success, and a specific migration-guidance
// error for a recognized-but-unimplemented switch.
func (c Catalog) ValidateSwitch(switchVal string) (string, error) {
	if switchVal == "" {
		switchVal = "none"
	}
	known := c.KnownSwitches()
	if stringIn(known, switchVal) {
		return switchVal, nil
	}
	if p := c.Providers.Lookup(switchVal); p != nil && p.Kind == providers.Switch && p.Planned != "" {
		return "", fmt.Errorf("%s is planned but unsupported in this slice; use --switch %s",
			switchVal, strings.Join(known, " or --switch "))
	}
	return "", fmt.Errorf("unknown --switch value: %s (known: %s)", switchVal, strings.Join(known, ", "))
}

// ValidateSelection validates a --components CSV and --switch value
// together.
func (c Catalog) ValidateSelection(clisCSV, switchVal string) error {
	if _, err := c.ValidateCLIs(clisCSV); err != nil {
		return err
	}
	if _, err := c.ValidateSwitch(switchVal); err != nil {
		return err
	}
	return nil
}

// selectionRefusal is the reference's login and
// runtime_provider_selection_refusal text for an unselected provider,
// adapted only by product name (codvps) and InstallCommand: a coding CLI
// names --components, a switch names --switch.
func (c Catalog) selectionRefusal(p *providers.Provider) string {
	if p.Kind == providers.Switch {
		return fmt.Sprintf("%s is not a selected switch; select it first, e.g.: sudo %s --switch %s", p.Name, InstallCommand, p.Name)
	}
	return fmt.Sprintf("%s is not a selected component; select it first, e.g.: sudo %s --components %s", p.Name, InstallCommand, c.exampleCLIs())
}

// RequireSelected returns the reference's actionable gating error if
// component is not a selected provider, or nil if it is.
func (c Catalog) RequireSelected(layout *paths.Layout, component string) error {
	reg, err := c.ReadAt(layout)
	if err != nil {
		return err
	}
	p := c.Providers.Lookup(component)
	if p == nil {
		return fmt.Errorf("unknown component: %s", component)
	}
	if !reg.Has(p) {
		return errors.New(c.selectionRefusal(p))
	}
	return nil
}

// RequireSelectedForHead returns the reference's head_set_enabled refusal
// for `codvps head enable <component>` when component is not selected, or
// nil if it is. It differs from the login wording ("rerun with it
// selected" rather than "select it first") because the reference words
// them differently. Disabling a head is never gated.
func (c Catalog) RequireSelectedForHead(layout *paths.Layout, component string) error {
	reg, err := c.ReadAt(layout)
	if err != nil {
		return err
	}
	p := c.Providers.Lookup(component)
	if p == nil {
		return fmt.Errorf("unknown component: %s", component)
	}
	if !reg.Has(p) {
		return fmt.Errorf("%s is not a selected component; rerun with it selected, e.g.: sudo %s --components %s", component, InstallCommand, c.exampleCLIs())
	}
	return nil
}

// Selected returns the gate for providers on this host: whether a
// provider name is selected in the registry at layout. An unreadable
// registry selects nothing, so no unselected provider is ever acted on.
func (c Catalog) Selected(layout *paths.Layout) func(string) bool {
	reg, err := c.ReadAt(layout)
	return func(name string) bool {
		p := c.Providers.Lookup(name)
		return err == nil && p != nil && reg.Has(p)
	}
}

// ResolveProviders resolves a `codvps update`-style provider word to the
// managed-runtime providers it names, mirroring the reference's
// runtime_resolve_providers. An empty string or "all" returns every
// *selected* provider in catalog order. A specific, known-but-unselected
// provider returns the reference's actionable refusal. An unrecognized word
// is a distinct "unknown runtime provider" error.
func (c Catalog) ResolveProviders(layout *paths.Layout, providerName string) ([]string, error) {
	reg, err := c.ReadAt(layout)
	if err != nil {
		return nil, err
	}
	managed := c.Providers.Where(func(p *providers.Provider) bool {
		return p.Planned == "" && p.Provision != nil && p.Provision.Update != nil
	})

	if providerName == "" || providerName == "all" {
		var out []string
		for _, p := range managed {
			if reg.Has(p) {
				out = append(out, p.Name)
			}
		}
		return out, nil
	}

	p := managed.Lookup(providerName)
	if p == nil {
		return nil, fmt.Errorf("unknown provider: %s (updatable: %s)", providerName, strings.Join(managed.Names(), ", "))
	}
	if !reg.Has(p) {
		return nil, errors.New(c.selectionRefusal(p))
	}
	return []string{providerName}, nil
}

// FreshHostNeedsSelection reports whether a fresh host needs an explicit
// component selection before `codvps install` proceeds, mirroring the
// reference's components_fresh_host_needs_selection: true only when there
// is no registry file, codvps was not installed on this host before, neither
// --components nor --switch was given, and there is no usable TTY to prompt
// on (hasTTY means "both stdin and stdout are a TTY", matching the
// reference's `[[ -t 0 ]] && [[ -t 1 ]]`, negated in the check below). It
// performs no filesystem writes.
func FreshHostNeedsSelection(layout *paths.Layout, installedBefore, hasComponentsFlag, hasSwitchFlag, hasTTY bool) (bool, error) {
	_, err := os.Stat(RegistryPath(layout))
	switch {
	case err == nil:
		return false, nil // registry exists: nothing to ask
	case !os.IsNotExist(err):
		return false, err
	}

	if installedBefore {
		return false, nil
	}
	if hasComponentsFlag || hasSwitchFlag {
		return false, nil
	}
	if hasTTY {
		return false, nil
	}
	return true, nil
}

// FreshHostSelectionMessage is the reference's die message for a fresh,
// flagless, TTY-less host (install.sh's own die call around
// components_fresh_host_needs_selection), adapted only by product name and
// InstallCommand.
func (c Catalog) FreshHostSelectionMessage() string {
	return fmt.Sprintf(
		"no component selection found on a fresh host with no TTY. Rerun with explicit flags, for example:\n  sudo %s --components %s --switch none",
		InstallCommand, c.exampleCLIs(),
	)
}

// binaryResolver finds a provider's vendor-installed CLI ("" when it is
// not at the vendor's install location).
type binaryResolver interface {
	ConfiguredBinary(provider string) (string, error)
}

// probeResult is the outcome of checking whether a component's binary is
// reachable through Runner.
type probeResult struct {
	installed bool
	failed    bool
	source    string // "vendor" or "path"
}

// resolveBinaryAndSource finds a provider's vendor-installed CLI,
// returning "" when there is none (the caller then falls back to a PATH
// lookup).
func resolveBinaryAndSource(resolver binaryResolver, p *providers.Provider) (string, error) {
	if resolver == nil || p.Provision == nil {
		return "", nil
	}
	return resolver.ConfiguredBinary(p.Name)
}

// probeInstalled runs "<binary> --version" through r and classifies the
// result the way the reference distinguishes "never installed" from "a
// pinned selection whose binary does not resolve" (component_state's
// runtime_unit_binary check): a lookup failure (binary not found on PATH)
// means never installed; any other failure to launch it (for example
// permission denied) means the installed component's binary is broken, one
// of the "failed" state below. A binary that runs but exits non-zero (a
// crashing or unrecognized --version flag) still counts as installed --
// it exists and launched, which is all this probe claims.
//
// If resolver is provided, the provider's vendor install location is
// checked first (source "vendor"); otherwise, or when nothing is there, a
// PATH lookup of the provider's binary is used (source "path").
func probeInstalled(r runner.Runner, resolver binaryResolver, p *providers.Provider) probeResult {
	var binary, source string

	if resolver != nil {
		b, err := resolveBinaryAndSource(resolver, p)
		if err != nil {
			return probeResult{failed: true, source: "vendor"}
		}
		if b != "" {
			binary = b
			source = "vendor"
		}
	}

	if binary == "" {
		binary = p.Binary
		source = "path"
	}

	_, _, _, err := r.Run(binary, "--version")
	if err == nil {
		return probeResult{installed: true, source: source}
	}
	msg := err.Error()
	if strings.Contains(msg, "not found") || strings.Contains(msg, "no such file") {
		return probeResult{installed: false, source: source}
	}
	if strings.Contains(msg, "exit status") {
		return probeResult{installed: true, source: source}
	}
	return probeResult{failed: true, source: source}
}

// ComponentState computes one of the reference's six component_state
// values for a single component: not-selected, selected-not-installed,
// installed-not-configured, ready, failed, or unsupported (a planned
// provider always, matching the reference's `cc-switch) printf
// 'unsupported\n'`). A provider with a credential is ready only once that
// credential is stored; one without is ready once installed. Resolver may
// be nil; when provided, managed runtime pins are checked first before
// falling back to PATH lookup.
func (c Catalog) ComponentState(layout *paths.Layout, r runner.Runner, reg *Registry, component string, resolver binaryResolver) string {
	p := c.Providers.Lookup(component)
	switch {
	case p == nil:
		return "unknown"
	case p.Planned != "":
		return "unsupported"
	case !reg.Has(p):
		return "not-selected"
	}
	probe := probeInstalled(r, resolver, p)
	if probe.failed {
		return "failed"
	}
	if !probe.installed {
		return "selected-not-installed"
	}
	if p.Credential != nil && !p.Credential.Configured(layout.Home()) {
		return "installed-not-configured"
	}
	return "ready"
}

// Report is the state of a single component for `codvps components status`
// / runtime_report-style output.
type Report struct {
	Component string
	State     string
	Source    string // "vendor" or "path"
}

// GetReport computes a Report for component. An unselected component always
// reports "not-selected", never "DRIFT" or "unmanaged" (the reference's
// runtime_report guard for a provider that install.sh was never asked to
// create). Resolver may be nil; when provided, managed runtime pins are
// checked first.
func (c Catalog) GetReport(layout *paths.Layout, r runner.Runner, component string, resolver binaryResolver) (*Report, error) {
	reg, err := c.ReadAt(layout)
	if err != nil {
		return nil, err
	}
	state := c.ComponentState(layout, r, reg, component, resolver)
	source := ""
	if p := c.Providers.Lookup(component); p != nil {
		source = resolveSource(resolver, p)
	}
	return &Report{Component: component, State: state, Source: source}, nil
}

// resolveSource returns which source was used to resolve a component's
// binary: "vendor" when it is at the vendor's install location, "path"
// otherwise. Returns "" if resolver is nil or the provider installs
// nothing.
func resolveSource(resolver binaryResolver, p *providers.Provider) string {
	if resolver == nil || p.Provision == nil {
		return ""
	}
	bin, err := resolver.ConfiguredBinary(p.Name)
	if err != nil || bin != "" {
		return "vendor"
	}
	return "path"
}

// List prints the reference's components_list output:
//
//	coding_clis: <csv or "none">
//	switch: <switch>
func (c Catalog) List(layout *paths.Layout) error {
	reg, err := c.ReadAt(layout)
	if err != nil {
		return err
	}
	csv := strings.Join(reg.Selected(), ",")
	if csv == "" {
		csv = "none"
	}
	fmt.Printf("coding_clis: %s\n", csv)
	fmt.Printf("switch: %s\n", reg.SwitchValue())
	return nil
}

// Status prints the reference's components_status table: one header line,
// then one row per provider, in catalog order. Managed runtime pins are
// checked first, with the source (managed or path) appended to the state
// column.
func (c Catalog) Status(layout *paths.Layout, r runner.Runner) error {
	reg, err := c.ReadAt(layout)
	if err != nil {
		return err
	}

	resolver := vendorResolver{set: c.Providers, home: layout.Home()}

	fmt.Printf("%-10s %-25s %s\n", "COMPONENT", "STATE", "CAPABILITIES("+strings.Join(providers.CapabilityNames, "/")+")")
	for _, p := range c.Providers {
		state := c.ComponentState(layout, r, reg, p.Name, resolver)
		// A binary source only means something for a selected component; an
		// unselected or unsupported row keeps the reference's exact text.
		source := resolveSource(resolver, p)
		if source != "" && state != "not-selected" {
			state = state + " (" + source + ")"
		}
		caps := make([]string, 0, len(providers.CapabilityNames))
		for _, name := range providers.CapabilityNames {
			caps = append(caps, name+"="+c.DescribeCapability(p.Name, name))
		}
		fmt.Printf("%-10s %-25s %s\n", p.Name, state, strings.Join(caps, " "))
	}
	return nil
}

// Usage is the reference's components_usage text, renamed only from the
// reference implementation's product name to codvps; the unsupported line
// names the planned providers.
func (c Catalog) Usage() string {
	var planned []string
	for _, p := range c.Providers.Where(func(p *providers.Provider) bool { return p.Planned != "" }) {
		planned = append(planned, p.Name)
	}
	return `Usage: codvps components <list|status>

  list     Show the selected coding CLIs and switch
  status   Show a computed state per component:
             not-selected            not chosen at install time
             selected-not-installed  chosen, but nothing is published/pinned yet
             installed-not-configured chosen and installed, but not authenticated
             ready                   chosen, installed, and configured
             failed                  chosen, but its runtime selection does not resolve
             unsupported             recognized but not implemented in this slice (` + strings.Join(planned, ", ") + `)
`
}

// vendorResolver is the production binaryResolver: each provider's
// Provision.Present locations.
type vendorResolver struct {
	set  providers.Set
	home string
}

func (v vendorResolver) ConfiguredBinary(provider string) (string, error) {
	if p := v.set.Lookup(provider); p != nil {
		return p.Installed("/", v.home), nil
	}
	return "", nil
}
