package head

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/runner"
)

// Reference scenario test_head_enable_claude_attaches_every_registered_repo.
func TestEnable_AttachesEveryRegisteredRepository(t *testing.T) {
	h := newTestHost(t)
	h.register("alpha", "beta")
	h.login()

	if err := h.claude.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	fi, err := os.Stat(h.layout.ClaudeHeadFlagPath())
	if err != nil {
		t.Fatalf("head flag missing: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("head flag mode = %o, want 600", fi.Mode().Perm())
	}
	for _, name := range []string{"alpha", "beta"} {
		if !h.trusted(name) {
			t.Errorf("workspace trust not seeded for %s", name)
		}
		if !h.unitUp(name) {
			t.Errorf("claude-remote@%s is not enabled and active", name)
		}
	}
	if len(h.sd.refusals) != 0 {
		t.Fatalf("Claude refused a workspace: %v", h.sd.refusals)
	}
	cfg, err := os.ReadFile(filepath.Join(h.layout.Home(), ".claude.json"))
	if err != nil || !strings.Contains(string(cfg), `"remoteDialogSeen": true`) {
		t.Fatalf("Remote Control consent not seeded: %v %s", err, cfg)
	}
	if got := len(h.sd.callsMatching("systemctl", "--user", "enable", "--now")); got != 2 {
		t.Fatalf("enable --now called %d times, want 2", got)
	}
}

// Reference scenario test_claude_head_seeds_workspace_trust: trust comes
// from `head enable claude` itself, and disable turns every unit off but
// keeps the repository registered.
func TestEnableSeedsTrustAndDisableStopsButKeepsRegistration(t *testing.T) {
	h := newTestHost(t)
	h.register("heritage")
	h.login()

	if err := h.claude.SetEnabled(false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	if h.trusted("heritage") {
		t.Fatalf("a disabled head seeded workspace trust")
	}
	if err := h.claude.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	if !h.trusted("heritage") || !h.unitUp("heritage") {
		t.Fatalf("enable did not seed trust and start the head")
	}

	if err := h.claude.SetEnabled(false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	if _, err := os.Stat(h.layout.ClaudeHeadFlagPath()); !os.IsNotExist(err) {
		t.Fatalf("disable left the head flag behind: %v", err)
	}
	if !h.unitDown("heritage") {
		t.Fatalf("disable left claude-remote@heritage enabled or active")
	}
	names, err := registry.Read(h.layout.RepositoriesPath())
	if err != nil || len(names) != 1 || names[0] != "heritage" {
		t.Fatalf("disabling a head deregistered repositories: %v %v", names, err)
	}
}

// Reference scenario test_claude_head_lifecycle_is_host_scoped.
func TestLifecycleIsHostScopedAndRoundTrips(t *testing.T) {
	h := newTestHost(t)
	h.register("hello", "heritage")
	h.login()

	for round := 0; round < 2; round++ {
		if err := h.claude.SetEnabled(true); err != nil {
			t.Fatalf("round %d enable: %v", round, err)
		}
		if !h.unitUp("hello") || !h.unitUp("heritage") {
			t.Fatalf("round %d: enable did not bring every repository up", round)
		}
		if err := h.claude.SetEnabled(false); err != nil {
			t.Fatalf("round %d disable: %v", round, err)
		}
		if !h.unitDown("hello") || !h.unitDown("heritage") {
			t.Fatalf("round %d: disable left a repository's head running", round)
		}
	}
}

func TestEnable_RequiresClaudeLoginAndChangesNothing(t *testing.T) {
	h := newTestHost(t)
	h.register("alpha")

	err := h.claude.SetEnabled(true)
	if err == nil || err.Error() != "run codvps login claude first" {
		t.Fatalf("SetEnabled(true) without a credential = %v", err)
	}
	if _, statErr := os.Stat(h.layout.ClaudeHeadFlagPath()); !os.IsNotExist(statErr) {
		t.Fatalf("a refused enable still switched the head on")
	}
	if len(h.sd.calls) != 0 {
		t.Fatalf("a refused enable touched systemd: %v", h.sd.calls)
	}
}

func TestEnable_RejectsCredentialThatIsNotARegularFile(t *testing.T) {
	h := newTestHost(t)
	if err := os.MkdirAll(h.layout.ClaudeCredentialsPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.claude.SetEnabled(true); err == nil || err.Error() != "run codvps login claude first" {
		t.Fatalf("a directory credential was accepted: %v", err)
	}
}

// Reference scenario
// test_claude_head_fails_loudly_when_a_workspace_is_untrusted: a head Claude
// keeps refusing must fail the command, be stopped and disabled, and leave
// its journal on stderr -- never crash-loop silently under Restart=always.
func TestEnable_FailsLoudlyAndStopsAHeadThatNeverComesUp(t *testing.T) {
	h := newTestHost(t)
	h.register("trust-gap")
	h.login()
	h.sd.refuse["trust-gap"] = true

	err := h.claude.SetEnabled(true)
	want := "Claude Remote Control for trust-gap failed to become active; the head was stopped to avoid a restart loop"
	if err == nil || err.Error() != want {
		t.Fatalf("SetEnabled(true) = %v, want %q", err, want)
	}
	if !h.unitDown("trust-gap") {
		t.Fatalf("a crash-looping head was left enabled or running")
	}
	if h.sleeps != activationAttempts {
		t.Fatalf("polled %d times, want %d", h.sleeps, activationAttempts)
	}
	if !strings.Contains(h.diag.String(), "Workspace not trusted") {
		t.Fatalf("the head's journal was not shown: %q", h.diag.String())
	}
	if len(h.sd.refusals) == 0 || h.sd.refusals[0] != filepath.Join(h.layout.Home(), "trust-gap") {
		t.Fatalf("Claude did not record the refusal: %v", h.sd.refusals)
	}

	// Once Claude accepts the seeded trust, the same enable brings it up.
	delete(h.sd.refuse, "trust-gap")
	if err := h.claude.SetEnabled(true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if !h.unitUp("trust-gap") {
		t.Fatalf("the repaired head did not come up")
	}
}

// Seeding must happen before any unit starts: the fake Claude checks
// ~/.claude.json at start time, exactly the regression where heads were
// refused because seeding had been removed.
func TestEnable_SeedsTrustBeforeStartingUnits(t *testing.T) {
	h := newTestHost(t)
	h.register("fresh")
	h.login()
	if err := h.claude.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	if len(h.sd.refusals) != 0 {
		t.Fatalf("a unit started before its workspace was trusted: %v", h.sd.refusals)
	}
}

func TestReconcile_SkipsAlreadyHealthyHeads(t *testing.T) {
	h := newTestHost(t)
	h.register("alpha")
	h.login()
	if err := h.claude.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	before := len(h.sd.callsMatching("systemctl", "--user", "enable", "--now"))
	if err := h.claude.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if after := len(h.sd.callsMatching("systemctl", "--user", "enable", "--now")); after != before {
		t.Fatalf("a healthy head was restarted (%d -> %d enable calls)", before, after)
	}
	// Trust is still reseeded for healthy heads: systemd may restart them.
	if !h.trusted("alpha") {
		t.Fatalf("trust missing after reconcile")
	}
}

func TestReconcile_PrunesInstancesThatShouldNotRun(t *testing.T) {
	h := newTestHost(t)
	h.register("alpha")
	h.login()
	// An orphan left behind by an earlier registry, enabled but unregistered.
	h.sd.units["claude-remote@ghost.service"] = &fakeUnit{loaded: true, enabled: true, active: true}

	if err := h.claude.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	if !h.unitDown("ghost") {
		t.Fatalf("an unregistered instance was left running")
	}
	if !h.unitUp("alpha") {
		t.Fatalf("the registered repository did not come up")
	}
}

// Stop/disable failures are errors, not `|| true`: a head that cannot be
// detached must not be reported as detached.
func TestReconcile_PropagatesPruneFailureButKeepsPruning(t *testing.T) {
	h := newTestHost(t)
	h.sd.units["claude-remote@stuck.service"] = &fakeUnit{loaded: true, enabled: true, active: true}
	h.sd.units["claude-remote@other.service"] = &fakeUnit{loaded: true, enabled: true, active: true}
	h.sd.fail["disable claude-remote@stuck.service"] = runner.Response{ExitCode: 1, Stderr: "Access denied"}

	err := h.claude.SetEnabled(false)
	if err == nil || !strings.Contains(err.Error(), "claude-remote@stuck.service") || !strings.Contains(err.Error(), "Access denied") {
		t.Fatalf("SetEnabled(false) = %v, want the stuck unit's failure", err)
	}
	if !h.unitDown("other") {
		t.Fatalf("one failing unit stopped the others from being pruned")
	}
}

func TestReconcile_PropagatesEnableFailure(t *testing.T) {
	h := newTestHost(t)
	h.register("alpha")
	h.login()
	h.sd.fail["enable claude-remote@alpha.service"] = runner.Response{ExitCode: 1, Stderr: "Unit claude-remote@.service not found."}

	err := h.claude.SetEnabled(true)
	if err == nil || !strings.Contains(err.Error(), "failed to start the Claude head for alpha") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("SetEnabled(true) = %v, want a descriptive start failure", err)
	}
}

// Reference scenario test_fixture_add_without_tty (Claude half): with the
// head off, reconciliation attaches nothing and seeds no trust.
func TestReconcile_HeadOffAttachesNothing(t *testing.T) {
	h := newTestHost(t)
	h.register("hello")
	h.login()

	if err := h.claude.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(h.sd.callsMatching("systemctl", "--user", "enable")) != 0 {
		t.Fatalf("a disabled head enabled a unit")
	}
	if _, err := os.Stat(filepath.Join(h.layout.Home(), ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("a disabled head wrote ~/.claude.json: %v", err)
	}
}

// With the head off there is nothing that needs a credential, so a missing
// login must not break repo add/remove.
func TestReconcile_HeadOffNeedsNoLogin(t *testing.T) {
	h := newTestHost(t)
	h.register("hello")
	if err := h.claude.Reconcile(); err != nil {
		t.Fatalf("Reconcile with the head off and no login: %v", err)
	}
}

func TestReconcile_InstanceListingFailureIsAnError(t *testing.T) {
	h := newTestHost(t)
	h.sd.fail["list-units --no-pager"] = runner.Response{ExitCode: 1, Stderr: "Failed to connect to bus: No medium found", Err: errString("exit 1")}

	err := h.claude.Reconcile()
	if err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Fatalf("Reconcile = %v, want the listing failure", err)
	}
}

// `systemctl list-unit-files <glob>` exits non-zero without output when
// nothing matches; that is "no instances", not a failure.
func TestInstances_SilentNoMatchIsEmpty(t *testing.T) {
	h := newTestHost(t)
	h.sd.fail["list-unit-files --no-pager"] = runner.Response{ExitCode: 1, Err: errString("exit 1")}
	got, err := h.claude.instances(true)
	if err != nil || len(got) != 0 {
		t.Fatalf("instances = %v, %v; want none", got, err)
	}
}

func TestInstances_FiltersTemplateAndDeduplicates(t *testing.T) {
	h := newTestHost(t)
	h.sd.units["claude-remote@a.service"] = &fakeUnit{loaded: true, enabled: true, active: true}
	h.sd.units["claude-remote@b.service"] = &fakeUnit{enabled: true}
	got, err := h.claude.instances(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].name != "a" || got[1].name != "b" || got[1].unit != "claude-remote@b.service" {
		t.Fatalf("instances = %+v", got)
	}
}

// A registered repository whose checkout vanished is deregistered and its
// head detached (disable, then stop) before anything else happens.
func TestReconcile_PrunesVanishedCheckout(t *testing.T) {
	h := newTestHost(t)
	h.register("kept", "vanished")
	if err := os.RemoveAll(filepath.Join(h.layout.Home(), "vanished")); err != nil {
		t.Fatal(err)
	}
	if err := h.claude.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	names, err := registry.Read(h.layout.RepositoriesPath())
	if err != nil || len(names) != 1 || names[0] != "kept" {
		t.Fatalf("registry after prune = %v, %v", names, err)
	}
	if len(h.sd.callsMatching("systemctl", "--user", "disable", "claude-remote@vanished.service")) != 1 ||
		len(h.sd.callsMatching("systemctl", "--user", "stop", "claude-remote@vanished.service")) != 1 {
		t.Fatalf("vanished checkout's head was not detached: %v", h.sd.calls)
	}
}

func TestSetEnabled_RefusesSymlinkedStateDir(t *testing.T) {
	h := newTestHost(t)
	h.login()
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(h.layout.ConfigDir()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, h.layout.ConfigDir()); err != nil {
		t.Fatal(err)
	}
	if err := h.claude.SetEnabled(true); err == nil || !strings.Contains(err.Error(), "must be a real directory") {
		t.Fatalf("SetEnabled(true) through a symlinked state dir = %v", err)
	}
}

func TestNewClaude_RequiresEveryDependency(t *testing.T) {
	if _, err := NewClaude(Options{}); err == nil {
		t.Fatal("NewClaude accepted missing dependencies")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
