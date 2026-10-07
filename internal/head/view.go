package head

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/runner"
)

// Host is one provider's host-scoped Remote Control head as `codvps head`
// and `codvps status` drive it. Each coding CLI's definition in
// internal/providers picks its head out of Heads; the renderers here only
// ever see this interface, so a new head needs no renderer change.
type Host interface {
	// SetEnabled switches the head on (after its own login and install
	// checks) or off.
	SetEnabled(enable bool) error
	// ListRows are the head's `codvps list` rows: its host row first,
	// then any per-repository rows.
	ListRows() ([]ListRow, error)
	// StatusRow is the head's row in the HOST HEAD table of `codvps
	// status`.
	StatusRow() (StatusRow, error)
}

// RepoReporter is implemented by a head that runs one unit per registered
// repository: its units fill the REPOSITORY table of `codvps status`.
type RepoReporter interface {
	RepoRows() ([]RepoRow, error)
}

// ListRow is one `codvps list` row; the HEAD column is the provider's
// name.
type ListRow struct {
	Scope, Target, Enabled, Active string
}

// StatusRow is one HOST HEAD row of `codvps status`; the HOST HEAD column
// is the provider's name. Note, when set, is printed on its own line
// below the row.
type StatusRow struct {
	Unit, SubState, Daemon, Note string
}

// RepoRow is one REPOSITORY row of `codvps status`.
type RepoRow struct {
	Repository, Active, SubState, Restarts string
}

// Named is a head with the name of the provider it belongs to.
type Named struct {
	Name string
	Host Host
}

// CodexView is the read-only slice of the Codex head that its list and
// status rows are built from (internal/head.Codex in production).
// Liveness is the daemon's control-socket probe, never systemd's view of
// the oneshot unit, which stays active after its daemon dies.
type CodexView interface {
	// EnabledState is the raw `systemctl is-enabled` answer for the Codex
	// unit ("" when there is none).
	EnabledState() (string, error)
	// DaemonStatus reports whether the Codex daemon is running and, when
	// it is not, why.
	DaemonStatus() (running bool, reason string, err error)
	// UnitState is the Codex unit's ActiveState and SubState ("" when
	// unknown).
	UnitState() (activeState, subState string, err error)
}

const headListRow = "%-10s %-10s %-24s %-16s %s\n"

// RenderList writes the exact `codvps list` table: every head's rows
// in provider order (each head's host row, then its per-repository rows).
func RenderList(w io.Writer, heads []Named) error {
	var b strings.Builder
	fmt.Fprintf(&b, headListRow, "HEAD", "SCOPE", "TARGET", "ENABLED", "ACTIVE")
	for _, h := range heads {
		rows, err := h.Host.ListRows()
		if err != nil {
			return err
		}
		for _, r := range rows {
			fmt.Fprintf(&b, headListRow, h.Name, r.Scope, r.Target, r.Enabled, r.Active)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// ListRows are the Claude host row, then one row per registered repository
// followed by any surviving claude-remote@ instance systemd still knows
// about, so an orphaned unit stays visible instead of disappearing.
func (c *Claude) ListRows() ([]ListRow, error) {
	names, err := repo.PruneVanishedCheckouts(c.layout, c.git, c, false)
	if err != nil {
		return nil, err
	}
	instances, err := c.instances(true)
	if err != nil {
		// The registered repositories can still be reported; say why any
		// orphaned instance may be missing rather than fail the listing (a
		// failed write of this stderr warning has nowhere to be reported).
		_, _ = fmt.Fprintf(c.diag, "codvps: warning: %v\n", err)
	}
	headEnabled, err := repo.ClaudeHeadFlagSet(c.layout)
	if err != nil {
		return nil, err
	}

	rows := []ListRow{{Scope: "host", Target: "-", Enabled: enabledLabel(headEnabled), Active: "-"}}
	listed := make(map[string]bool)
	targets := append([]string{}, names...)
	for _, inst := range instances {
		targets = append(targets, inst.name)
	}
	for _, name := range targets {
		if listed[name] {
			continue
		}
		listed[name] = true
		// Same boundary as everywhere else: never build a unit string from
		// a name that is not a valid repository name.
		if registry.ValidateName(name) != nil {
			continue
		}
		unit, err := repo.ClaudeUnit(name)
		if err != nil {
			return nil, err
		}
		rows = append(rows, ListRow{Scope: "repo", Target: name,
			Enabled: orDefault(c.unitQuery("is-enabled", unit), "unknown"),
			Active:  orDefault(c.unitQuery("is-active", unit), "unknown")})
	}
	return rows, nil
}

// StatusRow is the Claude host switch.
func (c *Claude) StatusRow() (StatusRow, error) {
	headEnabled, err := repo.ClaudeHeadFlagSet(c.layout)
	if err != nil {
		return StatusRow{}, err
	}
	return StatusRow{Unit: enabledLabel(headEnabled), SubState: "-", Daemon: "-"}, nil
}

// RepoRows are one row per registered repository and per loaded
// claude-remote@ instance, with its ActiveState, SubState and restart
// count.
func (c *Claude) RepoRows() ([]RepoRow, error) {
	names, err := repo.PruneVanishedCheckouts(c.layout, c.git, c, false)
	if err != nil {
		return nil, err
	}
	loaded, err := c.instances(false)
	if err != nil {
		// Same as ListRows: warn on stderr, still report what is known.
		_, _ = fmt.Fprintf(c.diag, "codvps: warning: %v\n", err)
	}
	units := make(map[string]string)
	for _, name := range names {
		unit, err := repo.ClaudeUnit(name)
		if err != nil {
			return nil, err
		}
		units[unit] = name
	}
	for _, inst := range loaded {
		units[inst.unit] = inst.name
	}
	ordered := make([]string, 0, len(units))
	for unit := range units {
		ordered = append(ordered, unit)
	}
	sort.Slice(ordered, func(i, j int) bool { return units[ordered[i]] < units[ordered[j]] })
	var rows []RepoRow
	for _, unit := range ordered {
		props := repo.ShowUnitProperties(c.r, true, unit, "ActiveState", "SubState", "NRestarts")
		rows = append(rows, RepoRow{Repository: units[unit],
			Active:   orDefault(props["ActiveState"], "not-found"),
			SubState: orDefault(props["SubState"], "-"),
			Restarts: orDefault(props["NRestarts"], "0")})
	}
	return rows, nil
}

// ListRows is the Codex host row.
func (c *Codex) ListRows() ([]ListRow, error) { return codexListRows(c) }

// StatusRow is the Codex unit and its daemon.
func (c *Codex) StatusRow() (StatusRow, error) { return codexStatusRow(c) }

func codexListRows(v CodexView) ([]ListRow, error) {
	enabled, err := v.EnabledState()
	if err != nil {
		return nil, err
	}
	running, _, err := v.DaemonStatus()
	if err != nil {
		return nil, err
	}
	return []ListRow{{Scope: "host", Target: "-", Enabled: orDefault(enabled, "unknown"), Active: runningLabel(running)}}, nil
}

func codexStatusRow(v CodexView) (StatusRow, error) {
	active, sub, err := v.UnitState()
	if err != nil {
		return StatusRow{}, err
	}
	running, reason, err := v.DaemonStatus()
	if err != nil {
		return StatusRow{}, err
	}
	row := StatusRow{Unit: orDefault(active, "not-found"), SubState: orDefault(sub, "-"), Daemon: runningLabel(running)}
	if !running && active == "active" {
		row.Note = "daemon unreachable from this session: " + orDefault(reason, "cause unknown")
	}
	return row, nil
}

// unitQuery returns the trimmed stdout of `systemctl --user <verb> <unit>`.
// is-enabled/is-active exit non-zero for a perfectly valid "disabled" or
// "inactive" answer, so the exit status is not an error here; an empty
// answer is rendered as "unknown" by the caller.
func (c *Claude) unitQuery(verb, unit string) string {
	out, _, _, _ := c.r.Run("systemctl", "--user", verb, unit)
	return strings.TrimSpace(out)
}

// Tool is an interactive CLI whose version `codvps status` reports.
type Tool struct {
	// Label names it in the report ("Claude").
	Label string
	// Binary is what the operator's PATH resolves.
	Binary string
}

// StatusOptions are what `codvps status` reports besides the heads' own
// rows.
type StatusOptions struct {
	// Heads are every provider's head, in provider order.
	Heads []Named
	// Interactive are the coding CLIs whose interactive versions are
	// reported, in provider order.
	Interactive []Tool
	// Tools runs the interactive CLIs' --version and loginctl.
	Tools runner.Runner
	// Operator is the account whose linger state is reported.
	Operator string
}

const statusRow = "%-24s %-14s %-14s %s\n"

// RenderStatus writes `codvps status`: linger and the interactive CLI
// versions, the host heads, then the
// per-repository units of every head that runs them. It is a read-only
// report, so every probe falls back to the reference's fallback label
// (unknown, unavailable, not-found, -, 0) instead of failing: a missing
// tool or unreachable manager is itself what the report has to show. Only
// a corrupt registry fails it.
func RenderStatus(w io.Writer, opts StatusOptions) error {
	if opts.Tools == nil {
		return fmt.Errorf("status: Tools is required")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Linger: %s\n", orDefault(lingerState(opts.Tools, opts.Operator), "unknown"))
	fmt.Fprintf(&b, "Node: %s\n", firstVersionLine(opts.Tools, "node"))
	// Labelled interactive: this is what the operator's PATH resolves, which
	// is not necessarily what the heads run.
	for _, t := range opts.Interactive {
		fmt.Fprintf(&b, "%s (interactive): %s\n", t.Label, firstVersionLine(opts.Tools, t.Binary))
	}
	fmt.Fprintf(&b, "GitHub CLI: %s\n", firstVersionLine(opts.Tools, "gh"))

	fmt.Fprintf(&b, "\n"+statusRow, "HOST HEAD", "UNIT", "SUBSTATE", "DAEMON")
	var repoRows []RepoRow
	for _, h := range opts.Heads {
		row, err := h.Host.StatusRow()
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, statusRow, h.Name, row.Unit, row.SubState, row.Daemon)
		if row.Note != "" {
			fmt.Fprintf(&b, "  (%s)\n", row.Note)
		}
		if r, ok := h.Host.(RepoReporter); ok {
			rows, err := r.RepoRows()
			if err != nil {
				return err
			}
			repoRows = append(repoRows, rows...)
		}
	}

	fmt.Fprintf(&b, "\n"+statusRow, "REPOSITORY", "ACTIVE", "SUBSTATE", "RESTARTS")
	for _, r := range repoRows {
		fmt.Fprintf(&b, statusRow, r.Repository, r.Active, r.SubState, r.Restarts)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// lingerState is `loginctl show-user <operator> -p Linger --value`; "" when
// it cannot be determined.
func lingerState(r runner.Runner, operator string) string {
	out, _, code, err := r.Run("loginctl", "show-user", operator, "-p", "Linger", "--value")
	if code != 0 || err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// firstVersionLine is the reference's first_version_line: the first line of
// `<tool> --version` (stdout, then stderr), "unknown" if it printed
// nothing, "unavailable" if it could not run or failed.
func firstVersionLine(r runner.Runner, tool string) string {
	stdout, stderr, code, err := r.Run(tool, "--version")
	if code != 0 || err != nil {
		return "unavailable"
	}
	first, _, _ := strings.Cut(stdout+stderr, "\n")
	return orDefault(first, "unknown")
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func runningLabel(running bool) string {
	if running {
		return "running"
	}
	return "unavailable"
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
