package install

import (
	"github.com/egginsect/codvps/internal/login"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/systemd"
)

// fakeProvider is a coding CLI that exists only in this test: adding it
// to the provider set is the whole change, and install, provisioning and
// uninstall pick it up.
func fakeProvider() *providers.Provider {
	unit := systemd.UnitTemplate{
		Name:        "fakecli-remote.service",
		InstallPath: filepath.Join(systemd.UserUnitDir, "fakecli-remote.service"),
		Content:     []byte("[Service]\nExecStart=/usr/local/bin/fakecli serve\n"),
	}
	return &providers.Provider{
		Name: "fakecli", Kind: providers.CodingCLI, Binary: "fakecli", Label: "Fakecli",
		Login: &providers.Login{Summary: "Authenticate fakecli", Command: login.CLI{Binary: "fakecli", Args: []string{"login"}}},
		Capabilities: providers.Capabilities{
			Install: providers.Yes, Configure: providers.No, Auth: providers.Yes, Update: providers.No,
			Remove: providers.No, Service: providers.Yes, NativeRemote: providers.No,
		},
		Credential: &providers.Credential{Path: ".fakecli/token", Stored: providers.NonEmptyFile},
		Provision: &providers.Provision{
			Title:   "fakecli (vendor script)",
			Present: []string{"/usr/bin/fakecli"},
			Run:     func(p providers.Provisioner) error { return p.Cmd("bash", "-c", "install-fakecli") },
		},
		Install: &providers.Install{
			Units: func() []systemd.UnitTemplate { return []systemd.UnitTemplate{unit} },
			Links: []string{"fakecli"},
			Kept:  func(home string) []string { return []string{filepath.Join(home, ".fakecli") + " (fakecli login)"} },
		},
	}
}

func TestAddedProviderFlowsThroughInstallAndUninstall(t *testing.T) {
	e, f := newProvisionEnv(t)
	writeFstab(e)
	e.opts.Providers = append(providers.All(), fakeProvider())
	e.tool("fakecli")
	if err := e.install("--components", "claude,fakecli"); err != nil {
		t.Fatalf("install: %v\n%s", err, e.diag.String())
	}
	out := e.out.String()
	for _, want := range []string{
		"  - fakecli (vendor script): skipped (installed on first codvps enable fakecli or login fakecli)",
		"component selection: coding CLIs=claude,fakecli switch=none",
		"  codvps login fakecli\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("install output lacks %q:\n%s", want, out)
		}
	}
	if f.ran("bash -c install-fakecli") {
		t.Errorf("install downloaded a coding CLI: %v", f.io)
	}
	unitPath := e.sys("etc/systemd/user/fakecli-remote.service")
	if got := readTestFile(t, unitPath); !strings.Contains(got, "fakecli serve") {
		t.Fatalf("the provider's unit was not installed: %q", got)
	}
	if target, err := os.Readlink(e.sys("usr/local/bin/fakecli")); err != nil || target != filepath.Join(e.home, ".local/bin/fakecli") {
		t.Fatalf("the provider's link = %q (%v)", target, err)
	}

	if err := e.uninstall("--dry-run"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DRY-RUN: would remove /etc/systemd/user/fakecli-remote.service", "DRY-RUN: would remove /usr/local/bin/fakecli (->", "(fakecli login)"} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("uninstall --dry-run lacks %q:\n%s", want, e.out.String())
		}
	}
	if err := e.uninstall(); err != nil {
		t.Fatal(err)
	}
	mustAbsent(t, unitPath)
	mustAbsent(t, e.sys("usr/local/bin/fakecli"))
}
