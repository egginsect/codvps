// Package head implements the host-scoped Remote Control heads: the
// `codvps head enable|disable|list|pair|ensure` commands and the head
// sections of `codvps status`. This file is the Claude head; the
// Codex head is codex.go, and reaches the renderers here only
// through the read-only CodexView seam (see view.go).
//
// The Claude head is one host-wide switch, not a per-repository setting:
// while it is enabled every registered repository runs its own
// claude-remote@<repo>.service instance under the systemd --user manager,
// and while it is disabled none does. Reconcile is the single path that
// makes the running instances match that rule, so `head enable`, `head
// disable`, `repo add` and `repo remove` cannot disagree about it.
package head

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egginsect/codvps/internal/claudeconfig"
	"github.com/egginsect/codvps/internal/fsutil"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/runner"
)

// Activation probing, matching the reference's wait_for_active_user_unit:
// up to 30 one-second polls, and a unit only counts as up after two
// consecutive active answers, so a crash-looping head that is briefly
// "active" between restarts is not mistaken for a healthy one.
const (
	activationAttempts        = 30
	activationInterval        = time.Second
	requiredConsecutiveActive = 2
	journalLines              = "30"
)

const (
	claudeUnitPrefix = "claude-remote@"
	claudeUnitSuffix = ".service"
	claudeUnitGlob   = "claude-remote@*.service"
)

// Options are the Claude head's injected dependencies.
type Options struct {
	// Layout resolves HOME and the codvps state paths.
	Layout *paths.Layout
	// Git inspects registered checkouts during registry pruning.
	Git runner.Runner
	// Systemd runs every systemctl/journalctl call (systemd --user scope).
	Systemd runner.Runner
	// Diag receives the journal excerpt of a head that failed to start.
	Diag io.Writer
	// Sleep waits between activation polls; tests pass a no-op.
	Sleep func(time.Duration)
}

// Claude manages the host-wide Claude head.
type Claude struct {
	layout *paths.Layout
	git    runner.Runner
	r      runner.Runner
	diag   io.Writer
	sleep  func(time.Duration)
}

// NewClaude builds a Claude head from opts; every dependency is required.
func NewClaude(opts Options) (*Claude, error) {
	switch {
	case opts.Layout == nil:
		return nil, errors.New("claude head: Layout is required")
	case opts.Git == nil:
		return nil, errors.New("claude head: Git runner is required")
	case opts.Systemd == nil:
		return nil, errors.New("claude head: Systemd runner is required")
	case opts.Diag == nil:
		return nil, errors.New("claude head: Diag writer is required")
	case opts.Sleep == nil:
		return nil, errors.New("claude head: Sleep is required")
	}
	return &Claude{layout: opts.Layout, git: opts.Git, r: opts.Systemd, diag: opts.Diag, sleep: opts.Sleep}, nil
}

// errNeedsLogin is the reference's login gate for enabling a head.
var errNeedsLogin = errors.New("run codvps login claude first")

// SetEnabled flips the host-wide Claude head switch and reconciles. Enabling
// requires a stored Claude login first; the switch is written before the
// reconcile, as in the reference, so a head that fails to start stays
// switched on and the next enable or repo add retries it.
func (c *Claude) SetEnabled(enable bool) error {
	flag := c.layout.ClaudeHeadFlagPath()
	if enable {
		if err := c.requireCredential(); err != nil {
			return err
		}
		if err := ensureStateDir(c.layout.ConfigDir()); err != nil {
			return err
		}
		if err := fsutil.AtomicWrite(flag, nil, 0o600); err != nil {
			return fmt.Errorf("failed to switch the Claude head on: %w", err)
		}
	} else if err := os.Remove(flag); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to switch the Claude head off: %w", err)
	}
	return c.Reconcile()
}

// Reconcile makes the claude-remote@ instances match the registry and the
// host-wide switch (the reference's reconcile_claude_head):
//
//  1. prune every instance systemd knows about that should not run -- all
//     of them when the head is off, which is what makes this the disable
//     path too;
//  2. with the head on, require the Claude login, seed Remote Control
//     consent and workspace trust for every registered repository in one
//     write of ~/.claude.json, then start each instance that is not already
//     healthy and wait for it to stay up.
//
// A head that does not stay up is stopped and disabled again and reported
// as an error, rather than left crash-looping under Restart=always.
func (c *Claude) Reconcile() error {
	names, err := repo.PruneVanishedCheckouts(c.layout, c.git, c, false)
	if err != nil {
		return err
	}
	enabled, err := repo.ClaudeHeadFlagSet(c.layout)
	if err != nil {
		return err
	}
	desired := make(map[string]bool, len(names))
	if enabled {
		for _, name := range names {
			desired[name] = true
		}
	}

	instances, err := c.instances(true)
	if err != nil {
		return err
	}
	// Keep pruning past one failure so a single stuck unit does not leave
	// every other stray instance running; report them all together.
	var pruneErrs []error
	for _, inst := range instances {
		if desired[inst.name] {
			continue
		}
		if err := c.disableNow(inst.unit); err != nil {
			pruneErrs = append(pruneErrs, err)
		}
	}
	if len(desired) == 0 {
		return errors.Join(pruneErrs...)
	}

	if err := c.attach(names); err != nil {
		return errors.Join(append(pruneErrs, err)...)
	}
	return errors.Join(pruneErrs...)
}

// attach seeds consent and trust, then brings every named head up.
func (c *Claude) attach(names []string) error {
	if err := c.requireCredential(); err != nil {
		return err
	}
	home := c.layout.Home()
	if err := claudeconfig.SeedRemoteConsent(home); err != nil {
		return fmt.Errorf("failed to seed Claude Remote Control consent: %w", err)
	}
	// Trust is seeded for every registered repository, already-healthy
	// heads included: a healthy head can still be restarted by systemd
	// later, and it would then be refused without it.
	workspaces := make([]string, 0, len(names))
	for _, name := range names {
		workspaces = append(workspaces, filepath.Join(home, name))
	}
	if err := claudeconfig.SeedWorkspaceTrust(home, workspaces); err != nil {
		return fmt.Errorf("failed to seed Claude workspace trust: %w", err)
	}

	for _, name := range names {
		unit, err := repo.ClaudeUnit(name)
		if err != nil {
			return err
		}
		// A head that is already up and persistently enabled has nothing
		// left to do; re-confirming it would make every mutation cost two
		// seconds per registered repository.
		if c.unitHealthy(unit) {
			continue
		}
		c.resetFailed(unit)
		if err := c.systemctl("enable", "--now", unit); err != nil {
			return fmt.Errorf("failed to start the Claude head for %s: %w", name, err)
		}
		if err := c.waitForActive(unit, "Claude Remote Control for "+name); err != nil {
			return err
		}
	}
	return nil
}

// DisableClaudeUnit stops and disables one repository's Claude head. It lets
// registry pruning detach a repository whose checkout vanished.
func (c *Claude) DisableClaudeUnit(name string) error {
	return repo.DisableAndStopClaudeUnit(c.r, name)
}

// requireCredential enforces the reference's has_claude_cred gate: the
// Claude CLI's OAuth credential must exist as a regular file.
func (c *Claude) requireCredential() error {
	fi, err := os.Stat(c.layout.ClaudeCredentialsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errNeedsLogin
		}
		return fmt.Errorf("failed to check the Claude credential: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return errNeedsLogin
	}
	return nil
}

// instance is one claude-remote@ unit systemd reported.
type instance struct {
	name string
	unit string
}

// instances lists every claude-remote@<name>.service systemd knows about,
// each once, in first-seen order. withUnitFiles also includes instances that
// are merely enabled (list-unit-files), not only loaded (list-units), so
// pruning cannot miss a unit that is enabled but not currently loaded.
// Instances of a template never exist as files, so systemd's own listing is
// the only authority for them.
func (c *Claude) instances(withUnitFiles bool) ([]instance, error) {
	queries := [][]string{{"--user", "list-units", claudeUnitGlob, "--all", "--plain", "--no-legend", "--no-pager"}}
	if withUnitFiles {
		queries = append(queries, []string{"--user", "list-unit-files", claudeUnitGlob, "--no-legend", "--no-pager"})
	}
	seen := make(map[string]bool)
	var out []instance
	for _, args := range queries {
		stdout, stderr, code, err := c.r.Run("systemctl", args...)
		if code != 0 || err != nil {
			// `list-unit-files <glob>` exits non-zero without a word when
			// nothing matches; only a failure that explains itself (a
			// missing user bus, say) is a real error.
			if strings.TrimSpace(stderr) == "" && strings.TrimSpace(stdout) == "" {
				continue
			}
			return nil, fmt.Errorf("failed to list %s instances (systemctl %s): %s",
				claudeUnitGlob, args[1], describeFailure(stderr, code, err))
		}
		for _, line := range strings.Split(stdout, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			unit := fields[0]
			if !strings.HasPrefix(unit, claudeUnitPrefix) || !strings.HasSuffix(unit, claudeUnitSuffix) {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(unit, claudeUnitPrefix), claudeUnitSuffix)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, instance{name: name, unit: unit})
		}
	}
	return out, nil
}

// unitHealthy reports whether unit is fully up and persistently enabled:
// ActiveState=active, SubState=running and UnitFileState exactly "enabled"
// (enabled-runtime does not survive a reboot). The explicit state triple is
// read instead of trusting is-active's exit code, which does not
// distinguish a crash loop from a running head. A failed probe reads as
// "not healthy", which only means the head is driven and waited on again.
func (c *Claude) unitHealthy(unit string) bool {
	props := repo.ShowUnitProperties(c.r, true, unit, "ActiveState", "SubState", "UnitFileState")
	return props["ActiveState"] == "active" && props["SubState"] == "running" && props["UnitFileState"] == "enabled"
}

// resetFailed clears a previous failure and start-rate state so enable
// --now is not refused by systemd's start limit. Its own failure is
// deliberately ignored: it fails for a unit that was never loaded or never
// failed, which is the normal first-attach case, and any real manager
// failure resurfaces on the enable --now that follows.
func (c *Claude) resetFailed(unit string) {
	_, _, _, _ = c.r.Run("systemctl", "--user", "reset-failed", unit)
}

func (c *Claude) isActive(unit string) bool {
	_, _, code, err := c.r.Run("systemctl", "--user", "is-active", "--quiet", unit)
	return code == 0 && err == nil
}

// waitForActive polls unit until it has been active twice in a row. On
// timeout it prints the unit's recent journal to Diag, stops and disables
// the unit so Restart=always cannot keep crash-looping it, and fails.
func (c *Claude) waitForActive(unit, label string) error {
	consecutive := 0
	for attempt := 1; attempt <= activationAttempts; attempt++ {
		if c.isActive(unit) {
			consecutive++
			if consecutive >= requiredConsecutiveActive {
				return nil
			}
		} else {
			consecutive = 0
		}
		c.sleep(activationInterval)
	}

	c.printJournal(unit)
	if err := c.disableNow(unit); err != nil {
		return fmt.Errorf("%s failed to become active, and stopping it failed too: %w", label, err)
	}
	return fmt.Errorf("%s failed to become active; the head was stopped to avoid a restart loop", label)
}

// printJournal copies the unit's last journal lines to Diag. It is a
// diagnostic aid only: if the journal cannot be read, that is said on Diag
// and the caller's own failure is still what gets returned. Diag (stderr)
// write errors are discarded: there is nowhere left to report them, and
// the caller's error still reaches the operator through the exit status.
func (c *Claude) printJournal(unit string) {
	stdout, stderr, code, err := c.r.Run("journalctl", "--user", "-u", unit, "-n", journalLines, "--no-pager")
	if code != 0 || err != nil {
		_, _ = fmt.Fprintf(c.diag, "codvps: could not read the journal for %s: %s\n", unit, describeFailure(stderr, code, err))
		return
	}
	_, _ = io.WriteString(c.diag, stdout)
}

func (c *Claude) disableNow(unit string) error {
	if err := c.systemctl("disable", "--now", unit); err != nil {
		return fmt.Errorf("failed to stop and disable %s: %w", unit, err)
	}
	return nil
}

// systemctl runs one state-changing `systemctl --user` call and turns any
// failure into an error carrying systemd's own explanation.
func (c *Claude) systemctl(args ...string) error {
	_, stderr, code, err := c.r.Run("systemctl", append([]string{"--user"}, args...)...)
	if code != 0 || err != nil {
		return fmt.Errorf("systemctl --user %s: %s", strings.Join(args, " "), describeFailure(stderr, code, err))
	}
	return nil
}

func describeFailure(stderr string, code int, err error) string {
	if msg := strings.TrimSpace(stderr); msg != "" {
		return msg
	}
	if err != nil {
		return fmt.Sprintf("exit %d: %v", code, err)
	}
	return fmt.Sprintf("exit %d", code)
}

// ensureStateDir creates the codvps state directory (0700) and refuses one
// that is a symlink or not a directory, like the reference's
// ensure_state_dir.
func ensureStateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create the codvps state directory %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("failed to inspect the codvps state directory %s: %w", dir, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("codvps state root must be a real directory: %s", dir)
	}
	return nil
}
