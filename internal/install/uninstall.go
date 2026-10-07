package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egginsect/codvps/internal/components"
	"github.com/egginsect/codvps/internal/systemd"
)

// UninstallUsage is `codvps uninstall`'s usage line.
const UninstallUsage = "codvps uninstall [--dry-run]"

// Uninstall runs `codvps uninstall [--dry-run]`: it stops and disables
// every provider's units and the managed-runtime watchers, then removes
// the fixed set of artifacts the provider definitions and install itself
// declare -- unit files, generated drop-ins, the published runtime tree
// and the /run drop boxes, the binary and the /usr/local/bin links -- and
// nothing else. A declared path that is no longer the kind of thing
// codvps puts there (a directory where a file belongs, a link that points
// elsewhere) is reported and left alone. Repositories, codvps state,
// credentials, the component registry and linger are kept, so `sudo
// codvps install` restores a working host. --dry-run prints the same plan
// without changing anything.
func Uninstall(opts Options, args []string) error {
	dryRun := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--dry-run":
		dryRun = true
	default:
		return fmt.Errorf("usage: %s", UninstallUsage)
	}
	h, err := resolveHost(opts, "uninstall")
	if err != nil {
		return err
	}
	u := &uninstaller{host: h, dryRun: dryRun}
	return u.execute()
}

type uninstaller struct {
	*host
	dryRun   bool
	failures []error
}

// DryRun implements providers.UninstallHost.
func (u *uninstaller) DryRun() bool { return u.dryRun }

func (u *uninstaller) execute() error {
	if u.dryRun {
		u.Printf("DRY-RUN: no changes will be made. Showing what would happen for operator: %s\n\n", u.op.Name)
	} else {
		u.Printf("Uninstalling codvps for operator: %s\n\n", u.op.Name)
	}

	for _, p := range u.installed() {
		if p.Install.Stop == nil {
			continue
		}
		p.Install.Stop(u)
		u.Printf("\n")
	}
	u.Printf("\n--- unit templates and drop-ins ---\n")
	for _, t := range u.productUnits() {
		u.removeFile(t.InstallPath)
	}
	for _, p := range u.installed() {
		if p.Install.StaleDropIns == nil {
			continue
		}
		for _, dropIn := range p.Install.StaleDropIns(u.op.Name) {
			// Install already removes these; this covers a host uninstalled
			// before it was upgraded. Only a file codvps generated goes.
			if data, err := os.ReadFile(u.Sys(dropIn.Path)); err != nil || !dropIn.Owns(data) {
				continue
			}
			u.removeFile(dropIn.Path)
			u.removeDirIfEmpty(filepath.Dir(dropIn.Path))
		}
	}
	u.Printf("\n--- retired pinned runtimes ---\n")
	u.removeLegacyRuntimes()
	u.reload()

	u.Printf("\n--- codvps CLI ---\n")
	u.removeFile(systemd.CodvpsBinary)

	u.Printf("\n--- system PATH symlinks ---\n")
	for _, name := range u.links() {
		u.removeLink(name)
	}

	if u.dryRun {
		u.Printf("\nDRY-RUN complete; nothing was removed.\n")
	} else if len(u.failures) == 0 {
		u.Printf("\nUninstall complete.\n")
	}
	u.printLeftInPlace()
	if len(u.failures) > 0 {
		return fmt.Errorf("uninstall incomplete: %w", errors.Join(u.failures...))
	}
	return nil
}

// links are every /usr/local/bin link install may have made, whether or
// not its provider is selected now.
func (u *uninstaller) links() []string {
	names := append([]string(nil), coreLinks...)
	for _, p := range u.installed() {
		names = append(names, p.Install.Links...)
	}
	return names
}

// removeFile removes a declared file. A directory in its place is not
// what codvps installs there, so it is reported and kept.
func (u *uninstaller) removeFile(path string) {
	fi, err := os.Lstat(u.Sys(path))
	if errors.Is(err, os.ErrNotExist) {
		u.Printf("%s: already absent.\n", path)
		return
	}
	if err != nil {
		u.failures = append(u.failures, fmt.Errorf("cannot inspect %s: %w", path, err))
		return
	}
	if fi.IsDir() {
		u.Warnf("skipping %s: it is a directory, not the file codvps installs there", path)
		return
	}
	if u.dryRun {
		u.Printf("DRY-RUN: would remove %s\n", path)
		return
	}
	if err := os.Remove(u.Sys(path)); err != nil {
		u.failures = append(u.failures, fmt.Errorf("failed to remove %s: %w", path, err))
		return
	}
	u.Printf("removed %s\n", path)
}

// removeDirIfEmpty removes a drop-in directory only when nothing else
// (site policy) lives in it.
func (u *uninstaller) removeDirIfEmpty(dir string) {
	entries, err := os.ReadDir(u.Sys(dir))
	if err != nil || len(entries) > 0 {
		return
	}
	if u.dryRun {
		u.Printf("DRY-RUN: would rmdir %s (empty)\n", dir)
		return
	}
	// A failure here means something appeared in the directory since it
	// was listed: that is site policy, which stays.
	if err := os.Remove(u.Sys(dir)); err == nil {
		u.Printf("removed %s\n", dir)
	}
}

func (u *uninstaller) reload() {
	if u.dryRun {
		u.Printf("DRY-RUN: would run systemctl daemon-reload\n")
		u.Printf("DRY-RUN: would run the operator systemctl --user daemon-reload\n")
		return
	}
	if err := u.Run("systemctl", "daemon-reload"); err != nil {
		u.failures = append(u.failures, err)
	}
	if err := u.RunUser("daemon-reload"); err != nil {
		u.Warnf("operator systemctl --user daemon-reload failed: %v", err)
	}
}

// removeLink removes /usr/local/bin/<name> when it is the symlink to
// ~/.local/bin/<name> that install makes. The immediate link target is
// compared, never the resolved chain: ~/.local/bin/codex is itself a link
// into the vendor tree. Anything else there is not codvps's link and is
// left alone.
func (u *uninstaller) removeLink(name string) {
	link := filepath.Join(localBin, name)
	want := u.linkTarget(name)
	fi, err := os.Lstat(u.Sys(link))
	if errors.Is(err, os.ErrNotExist) {
		u.Printf("%s: already absent.\n", link)
		return
	}
	if err != nil {
		u.failures = append(u.failures, fmt.Errorf("cannot inspect %s: %w", link, err))
		return
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		u.Warnf("skipping %s: not a symlink, so it is not the link codvps makes", link)
		return
	}
	target, _ := os.Readlink(u.Sys(link))
	if target != want {
		u.Warnf("skipping %s: points at '%s', not codvps's %s", link, target, want)
		return
	}
	if u.dryRun {
		u.Printf("DRY-RUN: would remove %s (-> %s)\n", link, target)
		return
	}
	if err := os.Remove(u.Sys(link)); err != nil {
		u.failures = append(u.failures, fmt.Errorf("failed to remove %s: %w", link, err))
		return
	}
	u.Printf("removed %s (-> %s)\n", link, target)
}

func (u *uninstaller) printLeftInPlace() {
	home := u.op.Home
	var b strings.Builder
	fmt.Fprintf(&b, "\nLeft in place (never touched by codvps uninstall):\n")
	fmt.Fprintf(&b, "  - all repository checkouts under %s\n", home)
	fmt.Fprintf(&b, "  - %s (codvps state: registry, head switches, runtime selection)\n", u.layout.ConfigDir())
	for _, p := range u.installed() {
		if p.Install.Kept == nil {
			continue
		}
		for _, line := range p.Install.Kept(home) {
			fmt.Fprintf(&b, "  - %s\n", line)
		}
	}
	fmt.Fprintf(&b, "  - %s (the component selection)\n", filepath.Join("/etc", u.layout.Namespace(), components.RegistryFileName))
	fmt.Fprintf(&b, "  - packages, node, gh and the vendor CLIs the bootstrap installer provisioned\n")
	fmt.Fprintf(&b, "  - the loginctl linger enabled for %s\n", u.op.Name)
	fmt.Fprintf(&b, "\nTo reinstall: sudo codvps install (from a codvps binary), or rerun the codvps installer.\n")
	for _, p := range u.installed() {
		if p.Install.UninstallNote != "" {
			fmt.Fprintf(&b, "\n%s", p.Install.UninstallNote)
		}
	}
	u.Printf("%s", b.String())
}
