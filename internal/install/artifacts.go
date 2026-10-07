package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/systemd"
)

// binaryArtifact is /usr/local/bin/codvps.
type binaryArtifact struct {
	source string
	// same is true when the running binary already is the install target.
	same bool
}

type artifactPlan struct {
	binary binaryArtifact
	units  []systemd.UnitTemplate
}

// ProductUnits is every unit file install writes, in install order: each
// provider's own units in registry order.
func ProductUnits(set providers.Set) []systemd.UnitTemplate {
	var units []systemd.UnitTemplate
	for _, p := range set {
		if p.Planned == "" && p.Install != nil && p.Install.Units != nil {
			units = append(units, p.Install.Units()...)
		}
	}
	return units
}

func (h *host) productUnits() []systemd.UnitTemplate { return ProductUnits(h.opts.Providers) }

// planArtifacts resolves the running binary and names, before anything is
// written, every existing file install is about to replace. install is
// the single manager of these paths: a file of the same name is codvps's
// to overwrite, so it is reported, never refused.
func (h *host) planArtifacts() (artifactPlan, error) {
	var plan artifactPlan
	src, err := h.opts.Executable()
	if err != nil {
		return plan, fmt.Errorf("cannot locate the running codvps binary: %w", err)
	}
	plan.binary.source = src
	target := h.Sys(systemd.CodvpsBinary)
	same, err := sameFile(src, target)
	if err != nil {
		return plan, err
	}
	plan.binary.same = same
	if !same {
		data, err := os.ReadFile(src)
		if err != nil {
			return plan, fmt.Errorf("cannot read the running codvps binary %s: %w", src, err)
		}
		h.reportReplacement(systemd.CodvpsBinary, data)
	}
	plan.units = h.productUnits()
	for _, u := range plan.units {
		h.reportReplacement(u.InstallPath, u.Content)
	}
	return plan, nil
}

// reportReplacement names an existing file at path that differs from what
// install writes there.
func (h *host) reportReplacement(path string, want []byte) {
	if _, err := os.Lstat(h.Sys(path)); err != nil {
		return
	}
	if identical, err := sameContent(h.Sys(path), want); err == nil && identical {
		return
	}
	h.Printf("replacing %s: it differs from what codvps installs there\n", path)
}

// installBinary places the running binary at /usr/local/bin/codvps (root,
// 0755), unless it already runs from there.
func (h *host) installBinary(b binaryArtifact) error {
	if b.same {
		return nil
	}
	f, err := os.Open(b.source)
	if err != nil {
		return fmt.Errorf("cannot read the running codvps binary %s: %w", b.source, err)
	}
	defer func() { _ = f.Close() }()
	target := h.Sys(systemd.CodvpsBinary)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("failed to create %s: %w", filepath.Dir(target), err)
	}
	if err := writeReader(target, f); err != nil {
		return err
	}
	h.Printf("installed %s\n", systemd.CodvpsBinary)
	return nil
}

// installUnits writes every embedded unit file (root, 0644).
func (h *host) installUnits(units []systemd.UnitTemplate) error {
	for _, u := range units {
		if err := writeFile(h.Sys(u.InstallPath), u.Content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// linkTooling links the operator's uv and uvx, and every selected
// provider's declared executables, into /usr/local/bin: the product units
// run with PATH=/usr/local/bin:/usr/bin:/bin and never source ~/.bashrc. A
// missing uv or uvx is skipped with a warning; a selected provider's
// executable that cannot be linked fails the install.
func (h *host) linkTooling(selected providers.Set) error {
	for _, name := range coreLinks {
		if _, err := h.linkIntoPath(name, false); err != nil {
			return err
		}
	}
	// A selected CLI is installed on first use (codvps enable or login),
	// which runs as the operator and cannot write /usr/local/bin, so its
	// link is made now even while its target does not exist yet.
	for _, p := range selected {
		for _, name := range p.Install.Links {
			if _, err := h.linkIntoPath(name, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// linkTarget is where /usr/local/bin/<name> points: the operator's own
// ~/.local/bin/<name>.
func (h *host) linkTarget(name string) string {
	return filepath.Join(h.op.Home, ".local", "bin", name)
}

// linkIntoPath points /usr/local/bin/<name> at ~/.local/bin/<name> and
// reports whether the link is in place afterwards. A missing target is
// skipped unless onDemand (the CLI is installed on first use). Whatever else is at
// /usr/local/bin/<name> is replaced and named: the path is codvps's.
func (h *host) linkIntoPath(name string, onDemand bool) (bool, error) {
	target := h.linkTarget(name)
	link := filepath.Join(localBin, name)
	if !isExecutable(target) {
		// Until the CLI is installed, a working executable already at the
		// link path is kept; the link is only made where nothing runs.
		if !onDemand || isExecutable(h.Sys(link)) {
			h.Warnf("%s is missing; skipping system PATH symlink for %s", target, link)
			return false, nil
		}
	}
	if fi, err := os.Lstat(h.Sys(link)); err == nil {
		cur, _ := os.Readlink(h.Sys(link))
		if fi.Mode()&os.ModeSymlink != 0 && cur == target {
			return true, nil
		}
		if fi.IsDir() {
			return false, fmt.Errorf("cannot link %s: it is a directory", link)
		}
		h.Printf("replacing %s with the link to %s\n", link, target)
	}
	if err := os.MkdirAll(h.Sys(localBin), 0o755); err != nil {
		return false, fmt.Errorf("failed to create %s: %w", localBin, err)
	}
	tmp := h.Sys(link) + ".codvps-new"
	_ = os.Remove(tmp) // a leftover from an interrupted run; absent is fine
	if err := os.Symlink(target, tmp); err != nil {
		return false, fmt.Errorf("failed to link %s: %w", link, err)
	}
	if err := os.Rename(tmp, h.Sys(link)); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup; the rename error is what is reported
		return false, fmt.Errorf("failed to link %s: %w", link, err)
	}
	return true, nil
}

// sameFile reports whether a and b are the same file; a missing b is not.
func sameFile(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, fmt.Errorf("cannot stat the running codvps binary %s: %w", a, err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("cannot stat %s: %w", b, err)
	}
	return os.SameFile(fa, fb), nil
}
