package head

import (
	"fmt"
	"strings"

	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/repo"
)

// ListRows reports the host and per-repository Cursor head status.
func (c *Cursor) ListRows() ([]ListRow, error) {
	names, err := repo.PruneVanishedCheckouts(c.layout, c.git, c, false)
	if err != nil {
		return nil, err
	}
	instances, err := c.instances(true)
	if err != nil {
		_, _ = fmt.Fprintf(c.diag, "codvps: warning: %v\n", err)
	}
	headEnabled, err := repo.CursorHeadFlagSet(c.layout)
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
		if registry.ValidateName(name) != nil {
			continue
		}
		unit, err := repo.CursorUnit(name)
		if err != nil {
			return nil, err
		}
		rows = append(rows, ListRow{Scope: "repo", Target: name,
			Enabled: orDefault(c.unitQuery("is-enabled", unit), "unknown"),
			Active:  orDefault(c.unitQuery("is-active", unit), "unknown")})
	}
	return rows, nil
}

// StatusRow reports the Cursor head status for `codvps status`.
func (c *Cursor) StatusRow() (StatusRow, error) {
	headEnabled, err := repo.CursorHeadFlagSet(c.layout)
	if err != nil {
		return StatusRow{}, err
	}

	instances, err := c.instances(false)
	if err != nil {
		return StatusRow{}, err
	}

	var active, failed int
	for _, inst := range instances {
		state := c.unitQuery("is-active", inst.unit)
		switch state {
		case "active":
			active++
		case "failed":
			failed++
		}
	}

	unit := "disabled"
	if headEnabled {
		unit = "enabled"
	}

	subState := "-"
	if active > 0 {
		subState = fmt.Sprintf("%d active", active)
	}

	note := ""
	if failed > 0 {
		note = fmt.Sprintf("failures: %d", failed)
	}

	return StatusRow{Unit: unit, SubState: subState, Daemon: "-", Note: note}, nil
}

// RepoRows is not implemented for Cursor yet.
func (c *Cursor) RepoRows() ([]RepoRow, error) {
	return nil, nil
}

// unitQuery queries a systemd unit property.
func (c *Cursor) unitQuery(prop, unit string) string {
	out, _, _, _ := c.r.Run("systemctl", "--user", prop, unit)
	return strings.TrimSpace(out)
}
