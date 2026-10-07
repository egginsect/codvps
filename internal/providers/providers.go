// Package providers is the one place each coding CLI codvps manages is
// described: its name, how it is selected (a coding CLI through
// --components, or a switch through --switch), what it supports, and every
// facet a command needs from it. Commands never branch on a provider's
// name; they iterate All() (or a Set a test builds) and use the facets a
// provider declares. A facet only some providers have is a nil field, so
// "does this provider have a head / a login / a managed runtime" is a nil
// check, never a name comparison.
//
// Adding a coding CLI means adding one definition file here (see
// claude.go, codex.go) and listing it in All(); the commands
// that iterate the registry (install, uninstall, login, head, update,
// runtime, status, doctor, components) pick it up unchanged.
package providers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Kind is how a provider is selected at install time.
type Kind int

const (
	// CodingCLI is selected through `codvps install --components` and
	// recorded in the component registry's coding_clis array.
	CodingCLI Kind = iota + 1
	// Switch is selected through `codvps install --switch` and recorded as
	// the component registry's single switch value.
	Switch
)

// Support is one capability's status in `codvps components status`.
type Support string

// The capability states the component registry reports.
const (
	Yes         Support = "yes"
	No          Support = "no"
	PlannedOnly Support = "planned"
)

// CapabilityNames are the capability dimensions, in the order `codvps
// components status` prints them.
var CapabilityNames = []string{"install", "configure", "auth", "update", "remove", "service", "native_remote"}

// Capabilities is what codvps supports for one provider.
type Capabilities struct {
	Install      Support
	Configure    Support
	Auth         Support
	Update       Support
	Remove       Support
	Service      Support
	NativeRemote Support
}

// Get returns the named capability's status; ok is false for a name that
// is not one of CapabilityNames.
func (c Capabilities) Get(name string) (Support, bool) {
	switch name {
	case "install":
		return c.Install, true
	case "configure":
		return c.Configure, true
	case "auth":
		return c.Auth, true
	case "update":
		return c.Update, true
	case "remove":
		return c.Remove, true
	case "service":
		return c.Service, true
	case "native_remote":
		return c.NativeRemote, true
	}
	return "", false
}

// AllPlanned returns the capability row of a recognized provider that is not
// implemented yet.
func AllPlanned() Capabilities {
	return Capabilities{
		Install: PlannedOnly, Configure: PlannedOnly, Auth: PlannedOnly, Update: PlannedOnly,
		Remove: PlannedOnly, Service: PlannedOnly, NativeRemote: PlannedOnly,
	}
}

var allPlanned = AllPlanned()

// Credential is where a provider's sign-in lives and what counts as one.
type Credential struct {
	// Path is the credential file relative to the operator's home.
	Path string
	// Stored reports whether the file at an absolute path is a usable
	// stored login.
	Stored func(path string) bool
}

// File is the credential's absolute path under home.
func (c *Credential) File(home string) string { return filepath.Join(home, c.Path) }

// Configured reports whether home holds a stored login.
func (c *Credential) Configured(home string) bool { return c.Stored(c.File(home)) }

// RegularFile is a credential that must be a real regular file: a symlink
// or anything else is not followed and does not count.
func RegularFile(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular()
}

// NonEmptyFile is a credential that must resolve to a non-empty regular
// file (symlinks followed).
func NonEmptyFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// Provider is one coding CLI or switch. Only Name, Kind and Capabilities
// are required; every other facet is optional and nil when the provider
// does not have it.
type Provider struct {
	// Name is the word every command uses: --components, --switch,
	// `codvps login <name>`, `codvps head enable <name>`, `codvps update
	// <name>`.
	Name string
	// Kind is how the provider is selected.
	Kind Kind
	// Planned, when set, marks a recognized provider that is not
	// implemented yet and names the ticket that owns it. A planned
	// provider can never be selected.
	Planned string
	// Capabilities are reported by `codvps components status`.
	Capabilities Capabilities
	// Binary is the executable an interactive shell runs.
	Binary string
	// ExecutablePaths are home-relative paths where the binary may be installed
	// (e.g., ".local/bin/agent", ".opencode/bin/opencode"). These are checked
	// before PATH to resolve the binary's absolute path.
	ExecutablePaths []string
	// Label is the provider's human name in reports ("Claude"); a coding
	// CLI's interactive version is reported under it.
	Label string
	// Credential is the provider's stored login, when it has one.
	Credential *Credential
	// Provision installs the vendor CLI during host provisioning.
	Provision *Provision
	// Install is the provider's part of install and uninstall; every
	// implemented provider has one.
	Install *Install
	// Login is `codvps login <name>`.
	Login *Login
	// Head is the provider's Remote Control head.
	Head *Head
	// Doctor are the provider's `codvps doctor` checks.
	Doctor Doctor
	// Env is what the provider's head must not inherit from the operator's
	// login shell.
	Env *Env
	// Internal are the `codvps internal` helpers the provider's units run,
	// by name.
	Internal map[string]Internal
}

// Env is the environment facet of a provider.
type Env struct {
	// Remove are the variables removed from the login-shell environment a
	// head starts with, after the shell ran, so an rc file cannot export
	// them back (`codvps internal shell-exec --provider <name>`).
	Remove []string
}

// Set is an ordered list of providers. All() is the production set; tests
// build their own.
type Set []*Provider

// All is every provider codvps knows, in the order commands report them.
func All() Set {
	return Set{claude(), codex(), cursor(), opencode(), ccSwitch()}
}

// Lookup returns the provider called name, or nil.
func (s Set) Lookup(name string) *Provider {
	for _, p := range s {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// Names are the providers' names, in order.
func (s Set) Names() []string {
	out := make([]string, 0, len(s))
	for _, p := range s {
		out = append(out, p.Name)
	}
	return out
}

// Implemented are the providers of kind that can be selected (not
// planned), in order.
func (s Set) Implemented(kind Kind) Set {
	var out Set
	for _, p := range s {
		if p.Kind == kind && p.Planned == "" {
			out = append(out, p)
		}
	}
	return out
}

// PlannedOf are the recognized-but-unimplemented providers of kind.
func (s Set) PlannedOf(kind Kind) Set {
	var out Set
	for _, p := range s {
		if p.Kind == kind && p.Planned != "" {
			out = append(out, p)
		}
	}
	return out
}

// Where are the providers for which keep is true, in order.
func (s Set) Where(keep func(*Provider) bool) Set {
	var out Set
	for _, p := range s {
		if keep(p) {
			out = append(out, p)
		}
	}
	return out
}

// Validate checks the definitions every command relies on: unique,
// non-empty names, a known kind, a complete capability row, and the facets
// an implemented provider must carry. It is what the registry test runs
// against All(), and what a new definition has to satisfy.
func (s Set) Validate() error {
	seen := map[string]bool{}
	var problems []string
	for _, p := range s {
		switch {
		case p == nil:
			problems = append(problems, "a nil provider")
			continue
		case p.Name == "" || strings.ContainsAny(p.Name, " ,=/"):
			problems = append(problems, fmt.Sprintf("provider name %q is empty or not a plain word", p.Name))
		case seen[p.Name]:
			problems = append(problems, "duplicate provider "+p.Name)
		}
		seen[p.Name] = true
		if p.Kind != CodingCLI && p.Kind != Switch {
			problems = append(problems, p.Name+": unknown kind")
		}
		for _, name := range CapabilityNames {
			if v, _ := p.Capabilities.Get(name); v != Yes && v != No && v != PlannedOnly {
				problems = append(problems, fmt.Sprintf("%s: capability %s is %q", p.Name, name, v))
			}
		}
		if p.Planned != "" {
			continue
		}
		if p.Binary == "" {
			problems = append(problems, p.Name+": an implemented provider needs a Binary")
		}
		if p.Capabilities.Auth == Yes && (p.Credential == nil || p.Login == nil) {
			problems = append(problems, p.Name+": auth=yes needs a Credential and a Login")
		}
		if p.Capabilities.NativeRemote == Yes && (p.Head == nil || p.Head.Host == nil || p.Head.Repo == nil) {
			problems = append(problems, p.Name+": native_remote=yes needs a Head with a Host and a Repo")
		}
		if p.Kind == CodingCLI && p.Label == "" {
			problems = append(problems, p.Name+": a coding CLI needs a Label")
		}
		for name, in := range p.Internal {
			if name == "" || in.Run == nil || in.Usage == "" {
				problems = append(problems, fmt.Sprintf("%s: internal helper %q needs a Run and a Usage", p.Name, name))
			}
		}
		if pv := p.Provision; p.Capabilities.Update == Yes && (pv == nil || pv.Run == nil || pv.Update == nil || len(pv.Present) == 0 ||
			(pv.Restart.Glob != "" && pv.Restart.Unit != nil)) {
			problems = append(problems, p.Name+": update=yes needs a Provision with Present, Run, Update and at most one of Restart.Glob and Restart.Unit")
		}
		if p.Install == nil {
			problems = append(problems, p.Name+": an implemented provider needs an Install facet")
		} else if p.Capabilities.Service == Yes && p.Install.Units == nil {
			problems = append(problems, p.Name+": service=yes needs Install.Units")
		}
		if p.Provision != nil && (p.Provision.Run == nil) == (p.Provision.Note == "") {
			problems = append(problems, p.Name+": Provision needs exactly one of Run and Note")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid provider definitions: %s", strings.Join(problems, "; "))
	}
	return nil
}
