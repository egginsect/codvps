package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egginsect/codvps/internal/systemd"
)

// The managed-runtime layer (pinned copies of each CLI under
// /opt/codvps/runtimes, drop-ins pinning the heads to them, an updater and
// a publisher) was removed: every CLI is now the vendor's own
// install. Install retires what an earlier install left, before the unit
// files are rewritten, so the reload that follows puts every head back on
// the vendor CLI; uninstall removes the same set.

// retireStaleDropIns removes the drop-ins an earlier codvps generated for a
// provider's units (StaleDropIns), and their directory when nothing else is
// in it. Only a file at the exact declared path whose contents codvps
// recognises as its own is removed; anything else there is site policy and
// stays, with a warning.
func (h *host) retireStaleDropIns() error {
	for _, p := range h.installed() {
		if p.Install.StaleDropIns == nil {
			continue
		}
		for _, d := range p.Install.StaleDropIns(h.op.Name) {
			path := h.Sys(d.Path)
			data, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return fmt.Errorf("cannot read the retired drop-in %s: %w", d.Path, err)
			}
			if !d.Owns(data) {
				h.Warnf("kept %s: it is not a file codvps generated", d.Path)
				continue
			}
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("failed to remove the retired drop-in %s: %w", d.Path, err)
			}
			h.Printf("removed the retired drop-in %s\n", d.Path)
			_ = os.Remove(filepath.Dir(path)) // only succeeds when empty
		}
	}
	return nil
}

// retireLegacyRuntimes stops the legacy watchers and removes the legacy
// files, drop-ins and trees. Every step is idempotent.
func (h *host) retireLegacyRuntimes() error {
	found := false
	for _, unit := range systemd.LegacyRuntimeUnits(h.op.Name) {
		// Ignored deliberately: a unit that was never installed or enabled
		// has nothing to stop.
		_, _, _, _ = h.opts.Runner.Run("systemctl", "disable", "--now", unit)
	}
	for _, path := range systemd.LegacyRuntimePaths(h.op.Name) {
		if _, err := os.Lstat(h.Sys(path)); err != nil {
			continue
		}
		found = true
		if err := os.Remove(h.Sys(path)); err != nil {
			return fmt.Errorf("failed to remove the pinned-runtime leftover %s: %w", path, err)
		}
		_ = os.Remove(filepath.Dir(h.Sys(path))) // only succeeds when empty
	}
	// The layout already resolves these under the system root.
	for _, tree := range []string{h.layout.RuntimeRootPath(), h.layout.RunPath()} {
		// A head started before this install still executes its copy; the
		// tree stays until it restarts onto the vendor CLI, or new sessions
		// it spawns from its own path would fail.
		if n := h.runningFrom(tree); n > 0 {
			found = true
			h.Warnf("kept %s: %d running process(es) still execute from it; restart those heads, then rerun sudo codvps install", tree, n)
			continue
		}
		removed, err := removeRealDir(tree)
		if err != nil {
			return fmt.Errorf("failed to remove %s: %w", tree, err)
		}
		found = found || removed
	}
	if found {
		h.Printf("removed the pinned runtime copies and their drop-ins; heads run the vendor-installed CLIs from their next restart\n")
	}
	return nil
}

// runningFrom counts the processes whose executable is under tree (the
// production path; tree may carry the test system root).
func (h *host) runningFrom(tree string) int {
	procRoot := h.opts.ProcRoot
	if procRoot == "" {
		procRoot = "/proc"
	}
	logical := "/" + strings.TrimPrefix(strings.TrimPrefix(tree, h.opts.Root), "/")
	entries, _ := os.ReadDir(procRoot)
	n := 0
	for _, e := range entries {
		exe, err := os.Readlink(filepath.Join(procRoot, e.Name(), "exe"))
		if err != nil {
			continue
		}
		exe = strings.TrimSuffix(exe, " (deleted)")
		if strings.HasPrefix(exe, logical+"/") || strings.HasPrefix(exe, tree+"/") {
			n++
		}
	}
	return n
}

// removeRealDir removes path when it is a real directory; a symlink or a
// file there is not codvps's tree and is refused.
func removeRealDir(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.IsDir() {
		return false, fmt.Errorf("%s is not a directory codvps created", path)
	}
	return true, os.RemoveAll(path)
}

// removeLegacyRuntimes is uninstall's half: the same set, reported per
// path and honouring --dry-run.
func (u *uninstaller) removeLegacyRuntimes() {
	for _, unit := range systemd.LegacyRuntimeUnits(u.op.Name) {
		if u.dryRun {
			continue
		}
		_, _, _, _ = u.opts.Runner.Run("systemctl", "disable", "--now", unit)
	}
	for _, path := range systemd.LegacyRuntimePaths(u.op.Name) {
		if _, err := os.Lstat(u.Sys(path)); err != nil {
			continue
		}
		u.removeFile(path)
		u.removeDirIfEmpty(filepath.Dir(path))
	}
	for _, tree := range []string{u.layout.RuntimeRootPath(), u.layout.RunPath()} {
		if _, err := os.Lstat(tree); err != nil {
			continue
		}
		if u.dryRun {
			u.Printf("DRY-RUN: would remove %s and everything in it\n", tree)
			continue
		}
		if _, err := removeRealDir(tree); err != nil {
			u.failures = append(u.failures, err)
			continue
		}
		u.Printf("removed %s\n", tree)
	}
}
