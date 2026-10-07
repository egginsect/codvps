package systemd

import (
	"path/filepath"
)

// LegacyRuntimeUnits are the system units of the removed managed-runtime
// layer, per operator instance: install and uninstall stop and remove them.
func LegacyRuntimeUnits(operator string) []string {
	return []string{
		"codvps-update-request@" + operator + ".path",
		"codvps-updater@" + operator + ".service",
		"codvps-runtime-publish@" + operator + ".path",
		"codvps-runtime-publish@" + operator + ".service",
	}
}

// LegacyRuntimePaths are the files the removed managed-runtime layer
// installed: its unit templates, the drop-ins that pinned each head to a
// copied release, and its tmpfiles snippet. Removing the drop-ins puts
// every head back on the vendor-installed CLI. (Its release tree and drop
// boxes are paths.Layout's RuntimeRootPath and RunPath.)
func LegacyRuntimePaths(operator string) []string {
	return []string{
		filepath.Join(SystemUnitDir, "codvps-updater@.service"),
		filepath.Join(SystemUnitDir, "codvps-update-request@.path"),
		filepath.Join(SystemUnitDir, "codvps-runtime-publish@.service"),
		filepath.Join(SystemUnitDir, "codvps-runtime-publish@.path"),
		UserUnitDir + "/claude-remote@.service.d/runtime.conf",
		filepath.Join(SystemUnitDir, CodexUnit(operator)+".d", "runtime.conf"),
		"/etc/tmpfiles.d/codvps-runtime.conf",
	}
}
