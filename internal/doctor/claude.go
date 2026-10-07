package doctor

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/egginsect/codvps/internal/claudeconfig"
	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/systemd"
)

// ClaudeSuite is the Claude Code provider's doctor checks: its CLI and
// runtime with the prerequisites, and its per-repository head at the end.
// name is the provider's name and binary its CLI.
func ClaudeSuite(name, binary string) Suite {
	var st claudeState
	return Suite{
		Prepare: func(d *Check) error {
			var err error
			st, err = d.collectClaudeState()
			return err
		},
		Install: func(d *Check) {
			d.checkClaudeCLI(binary)
		},
		Head: func(d *Check) { d.checkClaudeHead(st) },
	}
}

// claudeState is what doctor learns about the Claude head once and reuses.
type claudeState struct {
	// configured is true once any Claude head is a commitment: the host
	// switch is on, or some claude-remote@ unit is enabled or active.
	configured bool
	// units is every claude-remote@ instance enabled, wanted by
	// default.target, or active.
	units []string
}

func (d *Check) collectClaudeState() (claudeState, error) {
	headOn, err := repo.ClaudeHeadFlagSet(d.opts.Layout)
	if err != nil {
		return claudeState{}, err
	}
	st := claudeState{configured: headOn}
	// Every census probe is read-only; a failed listing contributes no
	// units, and the user-bus check reports an unreachable manager.
	units := map[string]bool{}
	out, _, _, _ := d.opts.Runner.Run("systemctl", "--user", "list-unit-files", "claude-remote@*.service", "--no-legend", "--no-pager")
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && isClaudeInstance(f[0]) && strings.HasPrefix(f[1], "enabled") {
			units[f[0]] = true
			st.configured = true
		}
	}
	out, _, _, _ = d.opts.Runner.Run("systemctl", "--user", "show", "default.target", "-p", "Wants", "--value")
	for _, unit := range strings.Fields(out) {
		if isClaudeInstance(unit) {
			units[unit] = true
		}
	}
	out, _, _, _ = d.opts.Runner.Run("systemctl", "--user", "list-units", "claude-remote@*.service", "--all", "--plain", "--no-legend", "--no-pager")
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 4 && isClaudeInstance(f[0]) && (f[2] == "active" || f[3] == "running") {
			units[f[0]] = true
			st.configured = true
		}
	}
	for u := range units {
		st.units = append(st.units, u)
	}
	sort.Strings(st.units)
	return st, nil
}

func isClaudeInstance(unit string) bool {
	return strings.HasPrefix(unit, "claude-remote@") && strings.HasSuffix(unit, ".service") && unit != systemd.ClaudeRemoteTemplate
}

func (d *Check) checkClaudeCLI(binary string) {
	if _, err := d.opts.LookPath(binary); err == nil && d.succeeds(binary, "--version") {
		d.Pass("%s --version runs", binary)
	} else {
		d.Fail("%s is missing or not runnable", binary)
	}
	slice := filepath.Join(systemd.UserUnitDir, systemd.ClaudeRemoteSlice)
	if fileExists(d.sys(slice)) {
		props := repo.ShowUnitProperties(d.opts.Runner, true, systemd.ClaudeRemoteSlice, "MemoryHigh", "MemoryMax", "CPUQuotaPerSecUSec")
		d.Pass("claude-remote.slice is installed (MemoryHigh=%s MemoryMax=%s CPUQuotaPerSecUSec=%s)",
			orUnknown(props["MemoryHigh"]), orUnknown(props["MemoryMax"]), orUnknown(props["CPUQuotaPerSecUSec"]))
	} else {
		d.Fail("claude-remote.slice is missing at %s", slice)
	}
}

// checkClaudeHead covers the Claude login, consent, the environment a
// head must not inherit, the installed unit, and every Claude instance.
func (d *Check) checkClaudeHead(st claudeState) {
	switch claudeCredentialState(d.home()) {
	case credentialRegular:
		d.Pass("Claude OAuth credential file is present")
	case credentialMissing:
		if st.configured {
			d.Fail("Claude OAuth credential file is absent; run codvps login claude")
		} else {
			d.Warn("Claude OAuth credential file is absent; run codvps login claude before enabling a Claude head")
		}
	default:
		d.Fail("Claude OAuth credential source must be a real regular file when present; run codvps login claude")
	}

	if claudeconfig.RemoteConsentSeeded(d.home()) {
		d.Pass("Remote Control consent is seeded")
	} else if st.configured {
		d.Fail("Claude Remote Control consent is not seeded; run codvps login claude")
	} else {
		d.Warn("Claude Remote Control consent is not seeded")
	}

	for _, name := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		_, set := d.opts.LookupEnv(name)
		switch {
		case !set:
			d.Pass("%s is unset", name)
		case st.configured:
			d.Fail("%s must be unset", name)
		default:
			d.Warn("%s is set but no Claude head is enabled", name)
		}
	}
	baseURL, set := d.opts.LookupEnv("ANTHROPIC_BASE_URL")
	switch {
	case !set || baseURL == "https://api.anthropic.com":
		d.Pass("ANTHROPIC_BASE_URL is unset or points to https://api.anthropic.com")
	case st.configured:
		d.Fail("ANTHROPIC_BASE_URL must be unset or exactly https://api.anthropic.com")
	default:
		d.Warn("ANTHROPIC_BASE_URL is non-default but no Claude head is enabled")
	}

	unitFile := filepath.Join(systemd.UserUnitDir, systemd.ClaudeRemoteTemplate)
	if fileHasLine(d.sys(unitFile), "UnsetEnvironment=ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_BASE_URL") {
		d.Pass("installed unit clears Anthropic environment variables")
	} else {
		d.Fail("installed unit is missing the required UnsetEnvironment line")
	}

	for _, unit := range st.units {
		name := strings.TrimSuffix(strings.TrimPrefix(unit, "claude-remote@"), ".service")
		switch {
		case registry.ValidateName(name) != nil:
			d.Warn("enabled or running unit has an invalid repository instance: %s", unit)
		case repo.IsPrimaryGitCheckout(d.opts.Runner, filepath.Join(d.home(), name)) != nil:
			d.Warn("enabled or running unit %s has no repository at %s", unit, filepath.Join(d.home(), name))
		default:
			// is-active exits non-zero for every state but active; the
			// printed state is the answer.
			out, _, _, _ := d.opts.Runner.Run("systemctl", "--user", "is-active", unit)
			switch active := strings.TrimSpace(out); active {
			case "active":
				d.Pass("repository %s has an active Claude unit", name)
				d.checkHeadShellPath(true, unit)
			case "failed":
				d.Fail("repository %s has a failed Claude unit", name)
				// The journal tail is context for the FAIL above; an
				// unreadable journal just adds nothing.
				journal, _, _, _ := d.opts.Runner.Run("journalctl", "--user", "-u", unit, "-n", "5", "--no-pager")
				_, _ = io.WriteString(d.out, journal)
			default:
				d.Fail("repository %s has an enabled Claude unit that is not active (%s)", name, orDefault(active, "unknown"))
			}
		}
	}
}

type credentialState int

const (
	credentialMissing credentialState = iota
	credentialRegular
	credentialInvalid
)

// claudeCredentialState is the reference's claude_credential_source_state:
// a symlink or non-regular file is invalid, never followed.
func claudeCredentialState(home string) credentialState {
	fi, err := os.Lstat(filepath.Join(home, ".claude", ".credentials.json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return credentialMissing
	case err != nil, fi.Mode()&os.ModeSymlink != 0, !fi.Mode().IsRegular():
		return credentialInvalid
	}
	return credentialRegular
}
