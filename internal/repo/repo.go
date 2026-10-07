// Package repo implements repository registration and lifecycle
// (add/list/remove).
//
// The registry file is the sole authority for which checkouts codvps
// manages: a directory under $HOME that merely looks like a managed
// checkout (a stray clone, a temp directory Codex itself created) is
// invisible to every operation here unless its name is present in the
// registry.
package repo

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/runner"
)

// RepoManager manages repository registration and lifecycle.
type RepoManager struct {
	layout *paths.Layout
	git    runner.Runner
	heads  []Head
}

// NewRepoManager creates a new RepoManager. git is used only for git
// subprocess calls (clone, remote inspection, worktree detection) and is a
// real runner.Runner in both production and hermetic tests (local bare
// repositories, no network); heads, in provider order, own every
// systemd/head effect.
func NewRepoManager(layout *paths.Layout, git runner.Runner, heads []Head) *RepoManager {
	return &RepoManager{layout: layout, git: git, heads: heads}
}

// RepoInfo represents one registered repository as shown by `repo list`.
type RepoInfo struct {
	Name string
	Path string
	// Cells are each head's column value, in head order.
	Cells []string
}

// detachEvery releases a vanished checkout from every head; it is the
// detacher PruneVanishedCheckouts calls.
type detachEvery []Head

func (d detachEvery) DisableClaudeUnit(name string) error {
	for _, h := range d {
		if err := h.Detach(name); err != nil {
			return err
		}
	}
	return nil
}

// Add clones or reattaches a repository and registers it. It rolls back a
// clean new clone if the registry write fails, then reconciles every
// enabled head. A head that fails to attach afterward is reported as an
// error, but the checkout and its registration are not undone: past
// registration, a head problem is not a bad add.
func (rm *RepoManager) Add(url string) error {
	name, err := registry.RepoNameFromURL(url)
	if err != nil {
		return fmt.Errorf("invalid repository URL: %w", err)
	}
	dest := filepath.Join(rm.layout.Home(), name)

	symlinked, err := hasSymlinkAncestor(rm.layout.Home(), dest)
	if err != nil {
		return fmt.Errorf("failed to inspect repository path %q: %w", dest, err)
	}
	if symlinked {
		return fmt.Errorf("repository path %q has a symlinked component and cannot be managed", dest)
	}

	fi, statErr := os.Lstat(dest)
	existed := statErr == nil
	switch {
	case existed && fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("repository path %q is a symlink, which is not allowed", dest)
	case existed && !fi.IsDir():
		return fmt.Errorf("repository path %q exists and is not a directory", dest)
	case existed:
		if err := validateExistingCheckoutForURL(rm.git, dest, url); err != nil {
			return err
		}
	case !os.IsNotExist(statErr):
		return fmt.Errorf("failed to inspect repository path %q: %w", dest, statErr)
	default:
		if err := os.MkdirAll(rm.layout.Home(), 0o700); err != nil {
			return fmt.Errorf("failed to prepare %q: %w", rm.layout.Home(), err)
		}
		if _, stderr, code, runErr := rm.git.Run("git", "clone", "--", url, dest); runErr != nil || code != 0 {
			return fmt.Errorf("failed to clone repository: %s", strings.TrimSpace(stderr))
		}
	}

	registryPath := rm.layout.RepositoriesPath()
	names, err := readNamesBounded(registryPath)
	if err != nil {
		return fmt.Errorf("failed to read registry: %w", err)
	}
	if !containsString(names, name) {
		names = append(names, name)
	}
	if err := registry.Write(registryPath, names); err != nil {
		if !existed {
			rm.rollbackClone(dest, name)
		}
		return fmt.Errorf("failed to update registry: %w", err)
	}

	// Registration succeeded and stays regardless of what happens below: a
	// head that fails to attach is a head problem, not a bad add.
	for _, h := range rm.heads {
		if err := h.Attach(); err != nil {
			return err
		}
	}
	return nil
}

// List returns every registered repository still on disk, pruning any
// vanished checkout first (see PruneVanishedCheckouts), which only
// detaches that checkout: no head is stopped or restarted.
func (rm *RepoManager) List() ([]RepoInfo, error) {
	names, err := PruneVanishedCheckouts(rm.layout, rm.git, detachEvery(rm.heads), true)
	if err != nil {
		return nil, err
	}
	infos := make([]RepoInfo, 0, len(names))
	for _, name := range names {
		infos = append(infos, RepoInfo{Name: name, Path: filepath.Join(rm.layout.Home(), name)})
	}
	for _, h := range rm.heads {
		if h.Column() == "" {
			continue
		}
		cells, err := h.Cells(names)
		if err != nil {
			return nil, err
		}
		for i := range infos {
			infos[i].Cells = append(infos[i].Cells, cells[i])
		}
	}
	return infos, nil
}

// RenderList writes the exact `repo list` table: a "Heads:" summary line,
// a blank line, a header row, and one row per registered repository.
func (rm *RepoManager) RenderList(w io.Writer) error {
	infos, err := rm.List()
	if err != nil {
		return err
	}

	summaries := make([]string, 0, len(rm.heads))
	columns := make([]string, 0, len(rm.heads))
	for _, h := range rm.heads {
		summary, err := h.Summary()
		if err != nil {
			return err
		}
		summaries = append(summaries, summary)
		if col := h.Column(); col != "" {
			columns = append(columns, col)
		}
	}
	if _, err := fmt.Fprintf(w, "Heads: %s\n\n", strings.Join(summaries, " ")); err != nil {
		return err
	}
	if _, err := io.WriteString(w, listRow("REPOSITORY", "PATH", columns)); err != nil {
		return err
	}
	for _, info := range infos {
		if _, err := io.WriteString(w, listRow(info.Name, info.Path, info.Cells)); err != nil {
			return err
		}
	}
	return nil
}

// listRow is one repo list line: every column but the last is padded.
func listRow(name, path string, cells []string) string {
	fields := append([]string{fmt.Sprintf("%-24s", name), fmt.Sprintf("%-40s", path)}, cells...)
	for i := 2; i < len(fields)-1; i++ {
		fields[i] = fmt.Sprintf("%-12s", fields[i])
	}
	return strings.TrimRight(strings.Join(fields, " "), " ") + "\n"
}

// Remove deregisters a repository and detaches it from every head. It is
// idempotent: removing a repository that is not registered and that no
// head still holds is a true no-op, but only when the state directory and
// registry file are themselves verified safe (real, operator-owned,
// mode 0700/0600) -- otherwise it falls through to the fail-closed path
// below. Revocation fails closed: every head's Revoke runs before any read
// or validation that can fail, and nothing here undoes it, so a failure
// downstream (registry write) leaves it revoked. The working directory
// itself is never deleted.
func (rm *RepoManager) Remove(name string) (RemoveResult, error) {
	var result RemoveResult
	if err := registry.ValidateName(name); err != nil {
		return result, err
	}

	attached := false
	for _, h := range rm.heads {
		held, err := h.Attached(name)
		if err != nil {
			return result, err
		}
		attached = attached || held
	}

	if !attached && stateSafetyOK(rm.layout) {
		names, err := readNamesBounded(rm.layout.RepositoriesPath())
		if err != nil {
			return result, fmt.Errorf("failed to read registry: %w", err)
		}
		if !containsString(names, name) {
			return result, nil
		}
	}

	var held []string
	for _, h := range rm.heads {
		notice, err := h.Revoke()
		if err != nil {
			return result, err
		}
		if notice != "" {
			result.Notices = append(result.Notices, notice)
		}
		if note := h.Held(); note != "" {
			held = append(held, note)
		}
	}
	for _, h := range rm.heads {
		if err := h.Detach(name); err != nil {
			return result, fmt.Errorf("failed to disable %s head for %q: %w", h.Label(), name, err)
		}
	}

	registryPath := rm.layout.RepositoriesPath()
	names, err := readNamesBounded(registryPath)
	if err != nil {
		return result, fmt.Errorf("failed to read registry: %w", err)
	}
	filtered := make([]string, 0, len(names))
	for _, n := range names {
		if n != name {
			filtered = append(filtered, n)
		}
	}
	if err := registry.Write(registryPath, filtered); err != nil {
		if len(held) > 0 {
			return result, fmt.Errorf("failed to update registry; %s: %w", strings.Join(held, "; "), err)
		}
		return result, fmt.Errorf("failed to update registry: %w", err)
	}

	for _, h := range rm.heads {
		if err := h.Resync(name); err != nil {
			return result, err
		}
	}
	return result, nil
}

// RemoveResult reports what a successful Remove did beyond deregistering.
type RemoveResult struct {
	// Notices are what the heads' revocation left for the operator to do,
	// in head order.
	Notices []string
}

// PruneVanishedCheckouts reads the registry and drops any entry whose
// checkout is no longer a primary Git repository under home, disabling
// that repository's Claude head unit first (the same order repo remove
// uses) and rewriting the registry. It fails closed -- without pruning
// anything -- if the registry itself contains an invalid or duplicate
// name: that is a corrupt-registry condition, not a vanished checkout. sortCaseInsensitive selects the
// case-insensitive ordering `repo list` depends on.
func PruneVanishedCheckouts(layout *paths.Layout, git runner.Runner, reconciler ClaudeUnitDisabler, sortCaseInsensitive bool) ([]string, error) {
	registryPath := layout.RepositoriesPath()
	names, err := readNamesBounded(registryPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read registry: %w", err)
	}

	if err := checkRegistryIntegrity(names); err != nil {
		return nil, err
	}

	var valid, stale []string
	for _, name := range names {
		dest := filepath.Join(layout.Home(), name)
		if isPrimaryGitCheckout(git, dest) == nil {
			valid = append(valid, name)
		} else {
			stale = append(stale, name)
		}
	}

	for _, name := range stale {
		if err := reconciler.DisableClaudeUnit(name); err != nil {
			return nil, fmt.Errorf("failed to disable stale Claude head for %q: %w", name, err)
		}
	}
	if len(stale) > 0 {
		if err := registry.Write(registryPath, valid); err != nil {
			return nil, fmt.Errorf("failed to prune registry: %w", err)
		}
	}

	sort.Strings(valid)
	if sortCaseInsensitive {
		sort.SliceStable(valid, func(i, j int) bool {
			return strings.ToLower(valid[i]) < strings.ToLower(valid[j])
		})
	}
	return valid, nil
}

// checkRegistryIntegrity fails closed if names contains an invalid or
// duplicate entry: a corrupt registry must not be silently cleaned up.
func checkRegistryIntegrity(names []string) error {
	for _, name := range names {
		if err := registry.ValidateName(name); err != nil {
			return fmt.Errorf("invalid repository name in registry: %q", name)
		}
	}
	sorted := append([]string{}, names...)
	sort.Strings(sorted)
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == sorted[i-1] {
			return fmt.Errorf("duplicate repository in registry: %q", sorted[i])
		}
	}
	return nil
}

// isPrimaryGitCheckout accepts only a primary checkout: dest must be a real
// directory (no symlink) whose .git is a real directory (never a symlink
// or a worktree/submodule gitfile) and whose git-common-dir resolves back
// to that same .git. It never executes anything found inside dest; git is
// invoked only via the injected runner (dest is passed as -C, not resolved
// through PATH lookups inside the checkout).
func isPrimaryGitCheckout(git runner.Runner, dest string) error {
	return IsPrimaryGitCheckout(git, dest)
}

// IsPrimaryGitCheckout is the reference's is_physical_git_repo, shared
// with doctor and the install-time legacy membership migration so every
// caller applies the same "primary checkout only" rule.
func IsPrimaryGitCheckout(git runner.Runner, dest string) error {
	fi, err := os.Lstat(dest)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("not a real directory: %s", dest)
	}
	gitPath := filepath.Join(dest, ".git")
	gitFi, err := os.Lstat(gitPath)
	if err != nil {
		return fmt.Errorf("not a Git checkout: %s", dest)
	}
	if gitFi.Mode()&os.ModeSymlink != 0 || !gitFi.IsDir() {
		return fmt.Errorf("repository %q is a linked worktree or submodule (.git is not a real directory)", dest)
	}
	out, stderr, code, err := git.Run("git", "-C", dest, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || code != 0 {
		return fmt.Errorf("could not resolve git-common-dir for %s: %s", dest, strings.TrimSpace(stderr))
	}
	commonDir := strings.TrimSpace(out)
	resolvedCommon, cErr := filepath.EvalSymlinks(commonDir)
	if cErr != nil {
		resolvedCommon = commonDir
	}
	resolvedGitDir, gErr := filepath.EvalSymlinks(gitPath)
	if gErr != nil {
		resolvedGitDir = gitPath
	}
	if resolvedCommon != resolvedGitDir {
		return fmt.Errorf("repository %q is a linked worktree (git-common-dir does not match .git)", dest)
	}
	return nil
}

// validateExistingCheckoutForURL rejects a non-primary checkout, requires
// the checkout's origin remote to canonically match url, and finally
// requires url itself to be reachable without prompting for credentials
// (the reference's closing `GIT_TERMINAL_PROMPT=0 git ls-remote` check):
// a checkout whose remote used to exist but has since vanished or gone
// unreachable must not be silently reattached.
func validateExistingCheckoutForURL(git runner.Runner, dest, url string) error {
	if err := isPrimaryGitCheckout(git, dest); err != nil {
		return err
	}
	out, stderr, code, err := git.Run("git", "-C", dest, "remote", "get-url", "origin")
	if err != nil || code != 0 {
		return fmt.Errorf("existing checkout has no origin remote: %s: %s", dest, strings.TrimSpace(stderr))
	}
	originURL := strings.TrimSpace(out)
	originIdentity, err := registry.CanonicalRemoteIdentity(originURL)
	if err != nil {
		return fmt.Errorf("could not validate origin identity for %s: %w", dest, err)
	}
	requestedIdentity, err := registry.CanonicalRemoteIdentity(url)
	if err != nil {
		return fmt.Errorf("could not validate requested repository identity: %w", err)
	}
	if originIdentity != requestedIdentity {
		return fmt.Errorf("existing checkout origin does not match requested repository: origin=%s requested=%s", originURL, url)
	}
	if _, lsStderr, lsCode, lsErr := git.Run("git", "ls-remote", "--", url); lsErr != nil || lsCode != 0 {
		return fmt.Errorf("requested repository remote is not reachable without prompting: %s: %s", url, strings.TrimSpace(lsStderr))
	}
	return nil
}

// rollbackClone removes a checkout Add just cloned, but only when it is
// still exactly the pristine checkout at the expected path: a dirty tree,
// an unexpected path, or a symlink means something else already touched
// it, so it is retained rather than destroyed.
func (rm *RepoManager) rollbackClone(dest, name string) {
	if dest != filepath.Join(rm.layout.Home(), name) {
		return
	}
	fi, err := os.Lstat(dest)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return
	}
	top, _, code, err := rm.git.Run("git", "-C", dest, "rev-parse", "--show-toplevel")
	if err != nil || code != 0 {
		return
	}
	resolvedTop, tErr := filepath.EvalSymlinks(strings.TrimSpace(top))
	resolvedDest, dErr := filepath.EvalSymlinks(dest)
	if tErr != nil || dErr != nil || resolvedTop != resolvedDest {
		return
	}
	status, _, sCode, sErr := rm.git.Run("git", "-C", dest, "status", "--porcelain", "--untracked-files=all", "--ignored")
	if sErr != nil || sCode != 0 || strings.TrimSpace(status) != "" {
		return
	}
	_ = os.RemoveAll(dest)
}

// hasSymlinkAncestor reports whether root itself, or any path component
// between root and target, is a symlink.
func hasSymlinkAncestor(root, target string) (bool, error) {
	if fi, err := os.Lstat(root); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return true, nil
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}

	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false, err
	}
	if rel == "." || rel == "" {
		return false, nil
	}

	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return true, nil
		}
	}
	return false, nil
}

// stateSafetyOK reports whether the codvps state directory and repository
// registry are both verified safe: real (not symlinks), owned by this
// process, and at the expected mode. Remove's idempotent no-op fast path
// may only be taken when this holds.
func stateSafetyOK(layout *paths.Layout) bool {
	return isSafeDir(layout.ConfigDir(), 0o700) && isSafeFile(layout.RepositoriesPath(), 0o600)
}

func isSafeDir(path string, mode os.FileMode) bool {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return false
	}
	if fi.Mode().Perm() != mode {
		return false
	}
	return ownedBySelf(fi)
}

func isSafeFile(path string, mode os.FileMode) bool {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return false
	}
	if fi.Mode().Perm() != mode {
		return false
	}
	return ownedBySelf(fi)
}

func ownedBySelf(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(st.Uid) == os.Geteuid()
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
