package providers

// cc-switch is a recognized switch that is not implemented yet: it is
// reported by `codvps components status` and refused by --switch with
// guidance, and has no facets.

func ccSwitch() *Provider {
	return &Provider{
		Name:         "cc-switch",
		Kind:         Switch,
		Planned:      "future",
		Capabilities: allPlanned,
	}
}
