package providers

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProvisioner records the vendor scripts it is asked to run; run
// stands in for their effect on disk.
type fakeProvisioner struct {
	home    string
	scripts []string
	run     func(script string) error
}

func (f *fakeProvisioner) Home() string                { return f.home }
func (f *fakeProvisioner) Cmd(string, ...string) error { return nil }
func (f *fakeProvisioner) AsOperator(_ []string, script string) error {
	f.scripts = append(f.scripts, script)
	if f.run != nil {
		return f.run(script)
	}
	return nil
}

// versionRunner answers `<bin> --version` and systemctl probes.
type versionRunner struct {
	version map[string]string
	answers map[string]string
	calls   []string
}

func (r *versionRunner) Run(name string, args ...string) (string, string, int, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, call)
	if len(args) == 1 && args[0] == "--version" {
		if v, ok := r.version[name]; ok {
			return v + "\n", "", 0, nil
		}
		return "", "not found", 127, os.ErrNotExist
	}
	return r.answers[call], "", 0, nil
}

func (r *versionRunner) RunWithIO(string, []string, io.Reader, io.Writer, io.Writer) (int, error) {
	return 0, nil
}

func fakeCLI(home string) *Provider {
	return &Provider{Name: "fakecli", Binary: "fake", Label: "Fake", Provision: &Provision{
		AsOperator: true,
		Present:    []string{"~/.local/bin/fake"},
		Run:        func(p Provisioner) error { return p.AsOperator(nil, "install fake") },
		Update:     func(p Provisioner, bin string) error { return p.AsOperator(nil, shellQuote(bin)+" update") },
		Restart:    Restart{User: true, Glob: "fake-remote@*.service"},
	}}
}

// A missing CLI is installed on first use and only then; an installer
// that leaves no executable is an error.
func TestEnsureInstalledOnFirstUse(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin", "fake")
	p := fakeCLI(home)
	prov := &fakeProvisioner{home: home}
	if _, err := EnsureInstalled(p, prov, "/"); err == nil || !strings.Contains(err.Error(), "no fake executable") {
		t.Fatalf("installer without effect = %v", err)
	}
	prov.run = func(string) error {
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			return err
		}
		return os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	}
	installed, err := EnsureInstalled(p, prov, "/")
	if err != nil || !installed || p.Installed("/", home) != bin {
		t.Fatalf("EnsureInstalled = %v, %v; installed at %q", installed, err, p.Installed("/", home))
	}
	prov.scripts = nil
	if installed, err := EnsureInstalled(p, prov, "/"); installed || err != nil || len(prov.scripts) != 0 {
		t.Fatalf("an installed CLI was reinstalled: %v %v %v", installed, err, prov.scripts)
	}
}

// Update runs the vendor updater on the installed binary and reports the
// version on each side; only active heads are restarted.
func TestUpdateRunsTheVendorUpdater(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin", "fake")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := fakeCLI(home)
	r := &versionRunner{version: map[string]string{bin: "fake 1.0.0"}, answers: map[string]string{
		"systemctl --user list-units fake-remote@*.service --plain --no-legend --no-pager": "fake-remote@a.service loaded active running\nfake-remote@b.service loaded inactive dead\n",
		"systemctl --user is-active fake-remote@a.service":                                 "active\n",
		"systemctl --user is-active fake-remote@b.service":                                 "inactive\n",
	}}
	prov := &fakeProvisioner{home: home, run: func(string) error { r.version[bin] = "fake 1.1.0"; return nil }}
	res, err := Update(p, prov, r, "/")
	if err != nil || res.Before != "fake 1.0.0" || res.After != "fake 1.1.0" || !res.Changed() {
		t.Fatalf("Update = %+v, %v", res, err)
	}
	if want := "'" + bin + "' update"; len(prov.scripts) != 1 || prov.scripts[0] != want {
		t.Fatalf("updater ran %v, want %q", prov.scripts, want)
	}
	units := ActiveUnits(p, r, "operator")
	if len(units) != 1 || units[0] != (Unit{Name: "fake-remote@a.service", User: true}) {
		t.Fatalf("active units = %v", units)
	}
	if err := RestartUnit(r, units[0]); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.calls[len(r.calls)-2:], "\n"); got != "systemctl --user reset-failed fake-remote@a.service\nsystemctl --user restart fake-remote@a.service" {
		t.Fatalf("restart calls = %s", got)
	}
	if err := RestartUnit(r, Unit{Name: "codex-remote@op.service"}); err != nil || r.calls[len(r.calls)-1] != "sudo systemctl restart codex-remote@op.service" {
		t.Fatalf("system unit restart = %v, %v", err, r.calls[len(r.calls)-1])
	}
}

// Every shipped provider installs and updates through its vendor, into
// the operator's home, so enable and login can install it without root.
func TestShippedProvidersInstallAsTheOperator(t *testing.T) {
	for _, p := range All().Where(func(p *Provider) bool { return p.Provision != nil && p.Provision.Update != nil }) {
		if !p.Provision.AsOperator {
			t.Errorf("%s installs as root", p.Name)
		}
		for _, path := range p.Provision.Present {
			if strings.Contains(path, "/opt/") {
				t.Errorf("%s is looked up in a managed copy: %s", p.Name, path)
			}
		}
	}
}
