package providers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every production definition carries the facets its capabilities
// promise, under a unique name, in the order commands report them.
func TestAllIsValid(t *testing.T) {
	all := All()
	if err := all.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(all.Names(), " "); got != "claude codex cursor opencode cc-switch" {
		t.Fatalf("registry order = %s", got)
	}
	if got := strings.Join(all.Implemented(CodingCLI).Names(), " "); got != "claude codex cursor opencode" {
		t.Fatalf("coding CLIs = %s", got)
	}
	if got := strings.Join(all.Implemented(Switch).Names(), " "); got != "" {
		t.Fatalf("switches = %s", got)
	}
	if got := strings.Join(all.PlannedOf(Switch).Names(), " "); got != "cc-switch" {
		t.Fatalf("planned switches = %s", got)
	}
	if all.Lookup("claude") == nil || all.Lookup("bogus") != nil {
		t.Fatal("Lookup does not find exactly the defined providers")
	}
	updatable := all.Where(func(p *Provider) bool { return p.Provision != nil && p.Provision.Update != nil })
	if got := strings.Join(updatable.Names(), " "); got != "claude codex cursor opencode" {
		t.Fatalf("vendor-updated providers = %s", got)
	}
	for _, p := range all.Implemented(CodingCLI) {
		if p.Login == nil || p.Head == nil || p.Doctor == nil || p.Provision == nil {
			t.Errorf("%s is missing a coding-CLI facet", p.Name)
		}
		if p.Capabilities.Update != Yes || p.Provision.Update == nil {
			t.Errorf("%s is not updated by its vendor updater", p.Name)
		}
	}
}

// A definition that promises a capability without the facet behind it,
// or reuses a name, is rejected with a reason naming it.
func TestValidateRejectsIncompleteDefinitions(t *testing.T) {
	good := func() *Provider {
		return &Provider{Name: "fake", Kind: CodingCLI, Binary: "fake", Label: "Fake", Capabilities: Capabilities{
			Install: Yes, Configure: No, Auth: No, Update: No, Remove: No, Service: No, NativeRemote: No,
		}, Install: &Install{}}
	}
	if err := (Set{good()}).Validate(); err != nil {
		t.Fatalf("a minimal definition was rejected: %v", err)
	}
	cases := map[string]func(p *Provider){
		"auth=yes needs a Credential and a Login": func(p *Provider) { p.Capabilities.Auth = Yes },
		"native_remote=yes needs a Head":          func(p *Provider) { p.Capabilities.NativeRemote = Yes },
		"a coding CLI needs a Label":              func(p *Provider) { p.Label = "" },
		"update=yes needs a Provision":            func(p *Provider) { p.Capabilities.Update = Yes },
		"needs a Binary":                          func(p *Provider) { p.Binary = "" },
		"needs an Install facet":                  func(p *Provider) { p.Install = nil },
		"service=yes needs Install.Units":         func(p *Provider) { p.Capabilities.Service = Yes },
		"exactly one of Run and Note":             func(p *Provider) { p.Provision = &Provision{Title: "x"} },
		"unknown kind":                            func(p *Provider) { p.Kind = 0 },
		`capability remove is ""`:                 func(p *Provider) { p.Capabilities.Remove = "" },
		`provider name "a b" is empty or not a`:   func(p *Provider) { p.Name = "a b" },
	}
	for want, breakIt := range cases {
		p := good()
		breakIt(p)
		if err := (Set{p}).Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: Validate = %v", want, err)
		}
	}
	if err := (Set{good(), good()}).Validate(); err == nil || !strings.Contains(err.Error(), "duplicate provider fake") {
		t.Errorf("duplicate: Validate = %v", err)
	}
}

// The credential predicates are the reference's two shapes: a real
// regular file (symlinks never followed), and a non-empty file.
func TestCredentialPredicates(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	full := filepath.Join(dir, "full")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(full, link); err != nil {
		t.Fatal(err)
	}
	if !RegularFile(empty) || !RegularFile(full) || RegularFile(link) || RegularFile(dir) || RegularFile(filepath.Join(dir, "none")) {
		t.Error("RegularFile must accept exactly real regular files")
	}
	if NonEmptyFile(empty) || !NonEmptyFile(full) || !NonEmptyFile(link) || NonEmptyFile(dir) {
		t.Error("NonEmptyFile must accept exactly non-empty regular files")
	}
	c := &Credential{Path: "full", Stored: NonEmptyFile}
	if !c.Configured(dir) || c.File(dir) != full {
		t.Errorf("Credential under %s = %s", dir, c.File(dir))
	}
}
