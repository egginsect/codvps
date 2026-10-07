package components

import (
	"encoding/json"
	"errors"
	"github.com/egginsect/codvps/internal/login"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/runner"
)

// newTestLayout builds a paths.Layout whose HOME is a fresh temp directory
// and whose system-wide paths (etc, opt, run, libexec) are relocated under a
// second temp directory via WithSystemRoot, so no test can ever touch the
// real /etc even if the code under test has a bug.
func newTestLayout(t *testing.T) *paths.Layout {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("CODVPS_NAMESPACE", "")

	layout, err := paths.New("")
	if err != nil {
		t.Fatalf("paths.New: %v", err)
	}
	return layout.WithSystemRoot(t.TempDir())
}

func writeRegistry(t *testing.T, layout *paths.Layout, reg *Registry) string {
	t.Helper()
	path := RegistryPath(layout)
	if err := Write(path, reg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return path
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(out)
}

// === Finding 4: no test may touch the real /etc =====================

func TestEtcPathStaysUnderSystemRoot(t *testing.T) {
	layout := newTestLayout(t)
	root := t.TempDir()
	layout = layout.WithSystemRoot(root)
	if !strings.HasPrefix(layout.EtcPath(), root) {
		t.Fatalf("EtcPath() = %q, want prefix %q (test must never resolve under the real /etc)", layout.EtcPath(), root)
	}
	if !strings.HasPrefix(RegistryPath(layout), root) {
		t.Fatalf("RegistryPath() = %q, want prefix %q", RegistryPath(layout), root)
	}
}

// === Finding 1: registry schema, strict validation, round trip =======

func TestRegistryRoundTripsExactReferenceBytes(t *testing.T) {
	raw := []byte(`{"version":1,"coding_clis":["claude","codex"],"switch":"none"}`)
	dir := t.TempDir()
	path := filepath.Join(dir, "components.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	reg, err := testCatalog.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if reg.Version != 1 {
		t.Errorf("Version = %d, want 1", reg.Version)
	}
	if !reflect.DeepEqual(reg.CodingCLIs, []string{"claude", "codex"}) {
		t.Errorf("CodingCLIs = %v, want [claude codex]", reg.CodingCLIs)
	}
	if reg.Switch != "none" {
		t.Errorf("Switch = %q, want none", reg.Switch)
	}

	// Round-trip: writing the parsed struct back out and reading it again
	// must reproduce the same values the reference bytes encoded.
	if err := Write(path, reg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	reg2, err := testCatalog.Read(path)
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}
	if !reflect.DeepEqual(reg, reg2) {
		t.Errorf("round trip mismatch: got %+v, want %+v", reg2, reg)
	}

	// The exact reference default document also round-trips.
	defaultRaw := []byte(`{"version":1,"coding_clis":[],"switch":"none"}`)
	defaultPath := filepath.Join(dir, "default.json")
	if err := os.WriteFile(defaultPath, defaultRaw, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	def, err := testCatalog.Read(defaultPath)
	if err != nil {
		t.Fatalf("Read default: %v", err)
	}
	if def.Version != 1 || len(def.CodingCLIs) != 0 || def.Switch != "none" {
		t.Errorf("default registry = %+v, want {1 [] none}", def)
	}
}

func TestReadRejectsWrongVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "components.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"coding_clis":[],"switch":"none"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := testCatalog.Read(path); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("Read with version=2 = %v, want a corrupt-registry error", err)
	}
}

func TestReadRejectsUnknownCLIInCodingClis(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "components.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"coding_clis":["notacli"],"switch":"none"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := testCatalog.Read(path); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("Read with unknown CLI = %v, want a corrupt-registry error", err)
	}
}

func TestReadRejectsInvalidSwitch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "components.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"coding_clis":[],"switch":"bogus"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := testCatalog.Read(path); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("Read with invalid switch = %v, want a corrupt-registry error", err)
	}
}

func TestReadRejectsNonArrayCodingClis(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "components.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"coding_clis":"claude","switch":"none"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := testCatalog.Read(path); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("Read with non-array coding_clis = %v, want a corrupt-registry error", err)
	}
}

func TestWriteCreatesRegistryAtMode0644(t *testing.T) {
	layout := newTestLayout(t)
	path := writeRegistry(t, layout, &Registry{CodingCLIs: []string{"claude", "codex"}, Switch: "none"})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 0644 (reference chmods the registry 0644; this port cannot chown to root unprivileged, so ownership is left to the caller running as root, same as the reference's best-effort chown)", info.Mode().Perm())
	}
}

func TestMissingRegistryNothingSelected(t *testing.T) {
	layout := newTestLayout(t)
	reg, err := testCatalog.Read(RegistryPath(layout))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if reg.IsSelected("claude") || reg.IsSelected("codex") {
		t.Errorf("missing registry should select nothing, got %+v", reg)
	}
}

func TestMissingRegistrySwitchDefaultsNone(t *testing.T) {
	layout := newTestLayout(t)
	reg, err := testCatalog.Read(RegistryPath(layout))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if reg.SwitchValue() != "none" {
		t.Errorf("SwitchValue() = %q, want none", reg.SwitchValue())
	}
}

func TestCorruptRegistryLoudFailure(t *testing.T) {
	layout := newTestLayout(t)
	path := RegistryPath(layout)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := testCatalog.Read(path)
	if err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("Read of corrupt registry = %v, want a corrupt-registry error", err)
	}
}

func TestRegistrySelectedReflectsRewrite(t *testing.T) {
	layout := newTestLayout(t)
	path := writeRegistry(t, layout, &Registry{CodingCLIs: []string{"claude", "codex"}, Switch: "none"})

	reg1, err := testCatalog.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reg1.IsSelected("claude") || !reg1.IsSelected("codex") || !reg1.switchSelected("none") {
		t.Fatalf("expected claude, codex and the none switch selected after first write, got %+v", reg1)
	}

	if err := Write(path, &Registry{CodingCLIs: []string{"claude"}, Switch: "none"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	reg2, err := testCatalog.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reg2.IsSelected("claude") {
		t.Errorf("claude should still be selected after rewrite")
	}
	if reg2.IsSelected("codex") {
		t.Errorf("codex should no longer be selected after rewrite")
	}
}

// === Finding 2: capability table pinned exactly to the reference =====

func TestCapabilityTableMatchesReference(t *testing.T) {
	// Transcribed from the reference's component_capability case statement:
	// one row per component, one column per capability.
	want := map[string]map[string]string{
		"claude": {
			"install": "yes", "configure": "yes", "auth": "yes",
			"update": "yes", "remove": "no", "service": "yes", "native_remote": "yes",
		},
		"codex": {
			"install": "yes", "configure": "yes", "auth": "yes",
			"update": "yes", "remove": "no", "service": "yes", "native_remote": "yes",
		},
		"cc-switch": {
			"install": "planned", "configure": "planned", "auth": "planned",
			"update": "planned", "remove": "planned", "service": "planned", "native_remote": "planned",
		},
	}

	for component, caps := range want {
		for capability, status := range caps {
			got := testCatalog.DescribeCapability(component, capability)
			if got != status {
				t.Errorf("testCatalog.DescribeCapability(%q, %q) = %q, want %q", component, capability, got, status)
			}
		}
	}

	// Every declared capability dimension is actually exercised by the table
	// above, so a future CapabilityNames addition cannot silently go
	// unpinned.
	for component, caps := range want {
		if len(caps) != len(providers.CapabilityNames) {
			t.Errorf("%s: table test covers %d capabilities, CapabilityNames has %d", component, len(caps), len(providers.CapabilityNames))
		}
	}
}

func TestCapabilityUnknownComponentOrAction(t *testing.T) {
	if got := testCatalog.DescribeCapability("bogus", "install"); got != "unknown" {
		t.Errorf("unknown component = %q, want unknown", got)
	}
	if got := testCatalog.DescribeCapability("claude", "bogus"); got != "unknown" {
		t.Errorf("unknown capability = %q, want unknown", got)
	}
}

// === Finding 3: components status order, unsupported, real probes ====

func TestStatusIteratesInReferenceOrderAndFormat(t *testing.T) {
	layout := newTestLayout(t)
	writeRegistry(t, layout, &Registry{CodingCLIs: []string{"claude"}, Switch: "none"})

	fake := runner.NewFakeRunner()
	fake.SetResponse("claude", []string{"--version"}, runner.Response{})
	// Give claude a configured credential file so it reports "ready".
	if err := os.MkdirAll(filepath.Join(layout.Home(), ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.Home(), ".claude", ".credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := testCatalog.Status(layout, fake); err != nil {
			t.Fatalf("Status: %v", err)
		}
	})

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("Status printed %d lines, want 6 (header + claude, codex, cursor, opencode, cc-switch): %q", len(lines), out)
	}
	wantHeader := "COMPONENT  STATE                     CAPABILITIES(install/configure/auth/update/remove/service/native_remote)"
	if lines[0] != wantHeader {
		t.Errorf("header = %q, want %q", lines[0], wantHeader)
	}

	wantOrder := []string{"claude", "codex", "cursor", "opencode", "cc-switch"}
	for i, name := range wantOrder {
		if !strings.HasPrefix(lines[i+1], name) {
			t.Errorf("row %d = %q, want to start with %q (registry order: claude, codex, cursor, opencode, cc-switch)", i, lines[i+1], name)
		}
	}

	if !strings.Contains(lines[1], "ready") {
		t.Errorf("claude row = %q, want state ready", lines[1])
	}
	if !strings.Contains(lines[2], "not-selected") {
		t.Errorf("codex row = %q, want state not-selected (codex was never in coding_clis)", lines[2])
	}
	if !strings.Contains(lines[3], "not-selected") {
		t.Errorf("cursor row = %q, want state not-selected (cursor was never in coding_clis)", lines[3])
	}
	if !strings.Contains(lines[4], "not-selected") {
		t.Errorf("opencode row = %q, want state not-selected (opencode was never in coding_clis)", lines[4])
	}
	wantCCSwitch := "cc-switch  unsupported               install=planned configure=planned auth=planned update=planned remove=planned service=planned native_remote=planned"
	if lines[5] != wantCCSwitch {
		t.Errorf("cc-switch row = %q, want %q", lines[5], wantCCSwitch)
	}
}

func TestComponentStateNotSelected(t *testing.T) {
	layout := newTestLayout(t)
	reg := DefaultRegistry()
	fake := runner.NewFakeRunner()
	if got := testCatalog.ComponentState(layout, fake, reg, "claude", nil); got != "not-selected" {
		t.Errorf("state = %q, want not-selected", got)
	}
	if len(fake.GetCalls()) != 0 {
		t.Errorf("an unselected component must never probe the runner, got calls %+v", fake.GetCalls())
	}
}

func TestComponentStateSelectedNotInstalled(t *testing.T) {
	layout := newTestLayout(t)
	reg := &Registry{CodingCLIs: []string{"claude"}}
	fake := runner.NewFakeRunner()
	fake.SetResponse("claude", []string{"--version"}, runner.Response{
		Err: errors.New(`exec: "claude": executable file not found in $PATH`),
	})
	if got := testCatalog.ComponentState(layout, fake, reg, "claude", nil); got != "selected-not-installed" {
		t.Errorf("state = %q, want selected-not-installed", got)
	}
}

func TestComponentStateInstalledNotConfigured(t *testing.T) {
	layout := newTestLayout(t)
	reg := &Registry{CodingCLIs: []string{"claude"}}
	fake := runner.NewFakeRunner()
	fake.SetResponse("claude", []string{"--version"}, runner.Response{Stdout: "1.2.3\n"})
	// No credential file written: not configured.
	if got := testCatalog.ComponentState(layout, fake, reg, "claude", nil); got != "installed-not-configured" {
		t.Errorf("state = %q, want installed-not-configured", got)
	}
}

func TestComponentStateReadyForClaude(t *testing.T) {
	layout := newTestLayout(t)
	reg := &Registry{CodingCLIs: []string{"claude"}}
	fake := runner.NewFakeRunner()
	fake.SetResponse("claude", []string{"--version"}, runner.Response{Stdout: "1.2.3\n"})
	if err := os.MkdirAll(filepath.Join(layout.Home(), ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.Home(), ".claude", ".credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := testCatalog.ComponentState(layout, fake, reg, "claude", nil); got != "ready" {
		t.Errorf("state = %q, want ready", got)
	}
}

func TestComponentStateReadyForCodexRequiresNonEmptyAuth(t *testing.T) {
	layout := newTestLayout(t)
	reg := &Registry{CodingCLIs: []string{"codex"}}
	fake := runner.NewFakeRunner()
	fake.SetResponse("codex", []string{"--version"}, runner.Response{Stdout: "1.2.3\n"})

	if err := os.MkdirAll(filepath.Join(layout.Home(), ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(layout.Home(), ".codex", "auth.json")

	// An empty auth.json is not credentials (mirrors the reference's `-s`
	// test, which requires the file to be non-empty, not merely present).
	if err := os.WriteFile(authPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := testCatalog.ComponentState(layout, fake, reg, "codex", nil); got != "installed-not-configured" {
		t.Errorf("state with empty auth.json = %q, want installed-not-configured", got)
	}

	if err := os.WriteFile(authPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := testCatalog.ComponentState(layout, fake, reg, "codex", nil); got != "ready" {
		t.Errorf("state with non-empty auth.json = %q, want ready", got)
	}
}

func TestComponentStateClaudeRejectsSymlinkedCredentials(t *testing.T) {
	layout := newTestLayout(t)
	reg := &Registry{CodingCLIs: []string{"claude"}}
	fake := runner.NewFakeRunner()
	fake.SetResponse("claude", []string{"--version"}, runner.Response{Stdout: "1.2.3\n"})

	claudeDir := filepath.Join(layout.Home(), ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(claudeDir, "elsewhere.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(claudeDir, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	if got := testCatalog.ComponentState(layout, fake, reg, "claude", nil); got != "installed-not-configured" {
		t.Errorf("state with symlinked credentials = %q, want installed-not-configured (a symlink is never a valid credential source)", got)
	}
}

func TestComponentStateFailedOnBrokenBinary(t *testing.T) {
	layout := newTestLayout(t)
	reg := &Registry{CodingCLIs: []string{"claude"}}
	fake := runner.NewFakeRunner()
	fake.SetResponse("claude", []string{"--version"}, runner.Response{
		Err: errors.New("fork/exec /opt/codvps/runtimes/claude/current/claude: permission denied"),
	})
	if got := testCatalog.ComponentState(layout, fake, reg, "claude", nil); got != "failed" {
		t.Errorf("state = %q, want failed", got)
	}
}

func TestComponentStateCCSwitchAlwaysUnsupported(t *testing.T) {
	layout := newTestLayout(t)
	fake := runner.NewFakeRunner()
	for _, reg := range []*Registry{DefaultRegistry(), {CodingCLIs: []string{"claude", "codex"}, Switch: "none"}} {
		if got := testCatalog.ComponentState(layout, fake, reg, "cc-switch", nil); got != "unsupported" {
			t.Errorf("state = %q, want unsupported", got)
		}
	}
	if len(fake.GetCalls()) != 0 {
		t.Errorf("cc-switch must never probe the runner, got calls %+v", fake.GetCalls())
	}
}

// === Finding 6: gating messages match the reference wording ==========

func TestRequireSelectedMessageMatchesReference(t *testing.T) {
	layout := newTestLayout(t)
	writeRegistry(t, layout, &Registry{CodingCLIs: []string{"claude"}, Switch: "none"})

	err := testCatalog.RequireSelected(layout, "codex")
	if err == nil {
		t.Fatal("expected an error for unselected codex")
	}
	want := "codex is not a selected component; select it first, e.g.: sudo codvps install --components claude,codex,cursor,opencode"
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}

	if err := testCatalog.RequireSelected(layout, "claude"); err != nil {
		t.Errorf("claude is selected, want no error, got %v", err)
	}
}

func TestResolveProvidersCodexUnselectedFails(t *testing.T) {
	layout := newTestLayout(t)
	writeRegistry(t, layout, &Registry{CodingCLIs: []string{"claude"}, Switch: "none"})

	_, err := testCatalog.ResolveProviders(layout, "codex")
	if err == nil {
		t.Fatal("expected an error")
	}
	want := "codex is not a selected component; select it first, e.g.: sudo codvps install --components claude,codex,cursor,opencode"
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}

func TestResolveProvidersClaudeOnClaudeOnlyHostResolves(t *testing.T) {
	layout := newTestLayout(t)
	writeRegistry(t, layout, &Registry{CodingCLIs: []string{"claude"}, Switch: "none"})

	got, err := testCatalog.ResolveProviders(layout, "claude")
	if err != nil {
		t.Fatalf("ResolveProviders: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"claude"}) {
		t.Errorf("got %v, want [claude]", got)
	}
}

func TestResolveProvidersAllReturnsSelectedInReferenceOrder(t *testing.T) {
	layout := newTestLayout(t)
	writeRegistry(t, layout, &Registry{CodingCLIs: []string{"claude"}, Switch: "none"})

	for _, name := range []string{"", "all"} {
		got, err := testCatalog.ResolveProviders(layout, name)
		if err != nil {
			t.Fatalf("testCatalog.ResolveProviders(%q): %v", name, err)
		}
		want := []string{"claude"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("testCatalog.ResolveProviders(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestResolveProvidersUnknownProviderFails(t *testing.T) {
	layout := newTestLayout(t)
	writeRegistry(t, layout, &Registry{CodingCLIs: []string{"claude"}, Switch: "none"})

	_, err := testCatalog.ResolveProviders(layout, "bogus")
	if err == nil {
		t.Fatal("expected an error")
	}
	want := "unknown provider: bogus (updatable: claude, codex, cursor, opencode)"
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}

// === Finding 7: FreshHostNeedsSelection mirrors the reference =========

func TestFreshHostNeedsSelectionTrueWhenFreshNoFlagsNoTTYNoBinary(t *testing.T) {
	layout := newTestLayout(t)
	needs, err := FreshHostNeedsSelection(layout, false, false, false, false)
	if err != nil {
		t.Fatalf("FreshHostNeedsSelection: %v", err)
	}
	if !needs {
		t.Error("expected true for a fresh, flagless, TTY-less host with no installed binary")
	}
}

func TestFreshHostNeedsSelectionFalseWhenBinaryInstalled(t *testing.T) {
	layout := newTestLayout(t)
	needs, err := FreshHostNeedsSelection(layout, true, false, false, false)
	if err != nil {
		t.Fatalf("FreshHostNeedsSelection: %v", err)
	}
	if needs {
		t.Error("expected false when the codvps binary is already installed")
	}
}

func TestFreshHostNeedsSelectionFalseWhenComponentsFlagGiven(t *testing.T) {
	layout := newTestLayout(t)
	needs, err := FreshHostNeedsSelection(layout, false, true, false, false)
	if err != nil {
		t.Fatalf("FreshHostNeedsSelection: %v", err)
	}
	if needs {
		t.Error("expected false when --components was given")
	}
}

func TestFreshHostNeedsSelectionFalseWhenSwitchFlagGiven(t *testing.T) {
	layout := newTestLayout(t)
	needs, err := FreshHostNeedsSelection(layout, false, false, true, false)
	if err != nil {
		t.Fatalf("FreshHostNeedsSelection: %v", err)
	}
	if needs {
		t.Error("expected false when --switch was given, even with no --components")
	}
}

func TestFreshHostNeedsSelectionFalseWhenTTYAvailable(t *testing.T) {
	layout := newTestLayout(t)
	needs, err := FreshHostNeedsSelection(layout, false, false, false, true)
	if err != nil {
		t.Fatalf("FreshHostNeedsSelection: %v", err)
	}
	if needs {
		t.Error("expected false when a TTY is available to prompt on")
	}
}

func TestFreshHostNeedsSelectionFalseWhenRegistryExists(t *testing.T) {
	layout := newTestLayout(t)
	writeRegistry(t, layout, &Registry{CodingCLIs: []string{"claude"}, Switch: "none"})

	needs, err := FreshHostNeedsSelection(layout, false, false, false, false)
	if err != nil {
		t.Fatalf("FreshHostNeedsSelection: %v", err)
	}
	if needs {
		t.Error("expected false once a registry file exists")
	}
}

func TestFreshHostSelectionMessageMatchesReference(t *testing.T) {
	want := "no component selection found on a fresh host with no TTY. Rerun with explicit flags, for example:\n  sudo codvps install --components claude,codex,cursor,opencode --switch none"
	if got := testCatalog.FreshHostSelectionMessage(); got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

// === Finding 8: components help|--help|-h ==============================

func TestUsageMatchesReferenceTextRenamedOnly(t *testing.T) {
	want := `Usage: codvps components <list|status>

  list     Show the selected coding CLIs and switch
  status   Show a computed state per component:
             not-selected            not chosen at install time
             selected-not-installed  chosen, but nothing is published/pinned yet
             installed-not-configured chosen and installed, but not authenticated
             ready                   chosen, installed, and configured
             failed                  chosen, but its runtime selection does not resolve
             unsupported             recognized but not implemented in this slice (cc-switch)
`
	if got := testCatalog.Usage(); got != want {
		t.Errorf("testCatalog.Usage() =\n%q\nwant\n%q", got, want)
	}
}

// === components list output format =====================================

func TestListOutputFormat(t *testing.T) {
	layout := newTestLayout(t)
	writeRegistry(t, layout, &Registry{CodingCLIs: []string{"codex", "claude"}, Switch: "none"})

	out := captureStdout(t, func() {
		if err := testCatalog.List(layout); err != nil {
			t.Fatalf("List: %v", err)
		}
	})
	want := "coding_clis: claude,codex\nswitch: none\n"
	if out != want {
		t.Errorf("testCatalog.List() output = %q, want %q", out, want)
	}
}

func TestListOutputFormatNothingSelected(t *testing.T) {
	layout := newTestLayout(t)
	out := captureStdout(t, func() {
		if err := testCatalog.List(layout); err != nil {
			t.Fatalf("List: %v", err)
		}
	})
	want := "coding_clis: none\nswitch: none\n"
	if out != want {
		t.Errorf("testCatalog.List() output = %q, want %q", out, want)
	}
}

// === Validation (ValidateCLIs / ValidateSwitch / ValidateSelection) ===

func TestValidateCLIsBoth(t *testing.T) {
	got, err := testCatalog.ValidateCLIs("claude,codex")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "claude,codex" {
		t.Errorf("got %q, want claude,codex", got)
	}
}

func TestValidateCLIsSingle(t *testing.T) {
	got, err := testCatalog.ValidateCLIs("claude")
	if err != nil || got != "claude" {
		t.Errorf("got (%q, %v), want (claude, nil)", got, err)
	}
}

func TestValidateCLIsNone(t *testing.T) {
	got, err := testCatalog.ValidateCLIs("none")
	if err != nil || got != "" {
		t.Errorf("got (%q, %v), want (\"\", nil)", got, err)
	}
}

func TestValidateCLIsEmpty(t *testing.T) {
	got, err := testCatalog.ValidateCLIs("")
	if err != nil || got != "" {
		t.Errorf("got (%q, %v), want (\"\", nil)", got, err)
	}
}

func TestValidateCLIsDedupe(t *testing.T) {
	got, err := testCatalog.ValidateCLIs("claude,claude")
	if err != nil || got != "claude" {
		t.Errorf("got (%q, %v), want (claude, nil)", got, err)
	}
}

func TestValidateCLIsUnknownDies(t *testing.T) {
	_, err := testCatalog.ValidateCLIs("claude,notacli")
	if err == nil {
		t.Fatal("expected an error for unknown CLI 'notacli'")
	}
	want := "unknown coding CLI in --components: notacli (known: claude codex cursor opencode)"
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}

func TestValidateSwitchNone(t *testing.T) {
	got, err := testCatalog.ValidateSwitch("none")
	if err != nil || got != "none" {
		t.Errorf("got (%q, %v), want (none, nil)", got, err)
	}
}

func TestValidateSwitchDefaultNone(t *testing.T) {
	got, err := testCatalog.ValidateSwitch("")
	if err != nil || got != "none" {
		t.Errorf("got (%q, %v), want (none, nil)", got, err)
	}
}

func TestValidateSwitchCCSwitchPlanned(t *testing.T) {
	_, err := testCatalog.ValidateSwitch("cc-switch")
	if err == nil {
		t.Fatal("expected an error for cc-switch")
	}
	if !strings.Contains(err.Error(), "planned") {
		t.Errorf("message = %q, want it to mention planned", err.Error())
	}
}

func TestValidateSwitchUnknownDies(t *testing.T) {
	_, err := testCatalog.ValidateSwitch("bogus")
	if err == nil {
		t.Fatal("expected an error for unknown switch 'bogus'")
	}
	want := "unknown --switch value: bogus (known: none)"
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}

func TestValidateSelectionCombinesBothChecks(t *testing.T) {
	if err := testCatalog.ValidateSelection("claude,codex", "none"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := testCatalog.ValidateSelection("notacli", "none"); err == nil {
		t.Error("expected an error for an invalid CLI list")
	}
	if err := testCatalog.ValidateSelection("claude", "bogus"); err == nil {
		t.Error("expected an error for an invalid switch")
	}
}

// === GetReport: unselected is never DRIFT/unmanaged ====================

func TestGetReportUnselectedIsNotSelectedNeverDrift(t *testing.T) {
	layout := newTestLayout(t)
	writeRegistry(t, layout, &Registry{CodingCLIs: []string{"codex"}, Switch: "none"})
	fake := runner.NewFakeRunner()

	report, err := testCatalog.GetReport(layout, fake, "claude", nil)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if report.State != "not-selected" {
		t.Errorf("State = %q, want not-selected", report.State)
	}
	if strings.Contains(report.State, "DRIFT") || strings.Contains(report.State, "unmanaged") {
		t.Errorf("State = %q must never be DRIFT or unmanaged for an unselected provider", report.State)
	}
}

// === Selected() sorts and dedupes ======================================

func TestRegistrySelectedSortsAndDedupes(t *testing.T) {
	reg := &Registry{CodingCLIs: []string{"codex", "claude", "codex", " ", ""}}
	got := reg.Selected()
	want := []string{"claude", "codex"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Selected() = %v, want %v", got, want)
	}
}

// Sanity check that json.Marshal of Registry actually agrees with the field
// order/names the reference uses, independent of the byte-level round-trip
// test above (belt and suspenders against an accidental json tag typo).
func TestRegistryJSONFieldNames(t *testing.T) {
	data, err := json.Marshal(&Registry{Version: 1, CodingCLIs: []string{"claude"}, Switch: "none"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"version", "coding_clis", "switch"} {
		if _, ok := m[key]; !ok {
			t.Errorf("marshaled registry is missing key %q: %s", key, data)
		}
	}
	if len(m) != 3 {
		t.Errorf("marshaled registry has %d keys, want exactly 3: %s", len(m), data)
	}
}

// Reference head_set_enabled: enabling the head of an unselected CLI is
// refused with its own wording; a selected CLI passes.
func TestRequireSelectedForHeadMessageMatchesReference(t *testing.T) {
	layout := newTestLayout(t)
	writeRegistry(t, layout, &Registry{CodingCLIs: []string{"codex"}, Switch: "none"})

	err := testCatalog.RequireSelectedForHead(layout, "claude")
	want := "claude is not a selected component; rerun with it selected, e.g.: sudo codvps install --components claude,codex,cursor,opencode"
	if err == nil || err.Error() != want {
		t.Fatalf("RequireSelectedForHead = %v, want %q", err, want)
	}
	if err := testCatalog.RequireSelectedForHead(layout, "codex"); err != nil {
		t.Fatalf("codex is selected, want no error, got %v", err)
	}
}

// tests/components.sh "source without jq" and the three fresh-host
// scenarios run with jq removed from PATH. codvps reads its registry with
// encoding/json, so the equivalent contract is that the whole registry
// path works with no executable on PATH at all: an empty PATH makes any
// attempt to shell out (to jq or anything else) fail loudly here.
func TestRegistryHandlingNeedsNoExternalTools(t *testing.T) {
	layout := newTestLayout(t)
	t.Setenv("PATH", t.TempDir())

	needs, err := FreshHostNeedsSelection(layout, false, false, false, false)
	if err != nil || !needs {
		t.Fatalf("FreshHostNeedsSelection on a fresh host = %v, %v; want true, nil", needs, err)
	}
	if err := testCatalog.ValidateSelection("claude,codex", "none"); err != nil {
		t.Fatalf("ValidateSelection: %v", err)
	}
	writeRegistry(t, layout, &Registry{Version: RegistryVersion, CodingCLIs: []string{"claude"}, Switch: "none"})
	reg, err := testCatalog.Read(RegistryPath(layout))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reg.IsSelected("claude") || reg.IsSelected("codex") || reg.SwitchValue() != "none" {
		t.Fatalf("Read = %+v, want claude selected, codex not, switch none", reg)
	}
	if err := testCatalog.RequireSelected(layout, "claude"); err != nil {
		t.Fatalf("testCatalog.RequireSelected(claude): %v", err)
	}
	needs, err = FreshHostNeedsSelection(layout, false, false, false, false)
	if err != nil || needs {
		t.Fatalf("FreshHostNeedsSelection with a registry = %v, %v; want false, nil", needs, err)
	}
}

// testCatalog is the production provider set.
var testCatalog = Catalog{Providers: providers.All()}

// A provider added to the catalog needs no change here: it becomes a
// valid --components value, is read back from the registry, gated, and
// reported by list and status like the built-in ones.
func TestAddedProviderFlowsThroughSelectionAndStatus(t *testing.T) {
	fake := &providers.Provider{
		Name: "fakecli", Kind: providers.CodingCLI, Binary: "fakecli", Label: "Fakecli",
		Login: &providers.Login{Summary: "Authenticate fakecli", Command: login.CLI{Binary: "fakecli", Args: []string{"login"}}},
		Capabilities: providers.Capabilities{
			Install: providers.Yes, Configure: providers.No, Auth: providers.Yes, Update: providers.No,
			Remove: providers.No, Service: providers.No, NativeRemote: providers.No,
		},
		Credential: &providers.Credential{Path: ".fakecli/token", Stored: providers.NonEmptyFile},
		Install:    &providers.Install{},
	}
	cat := Catalog{Providers: append(providers.All(), fake)}
	if err := cat.Providers.Validate(); err != nil {
		t.Fatal(err)
	}
	if csv, err := cat.ValidateCLIs("claude,fakecli"); err != nil || csv != "claude,fakecli" {
		t.Fatalf("ValidateCLIs = %q, %v", csv, err)
	}
	if _, err := testCatalog.ValidateCLIs("fakecli"); err == nil {
		t.Fatal("the production catalog accepted a provider it does not define")
	}

	layout := newTestLayout(t)
	writeRegistry(t, layout, &Registry{CodingCLIs: []string{"fakecli"}, Switch: "none"})
	if err := cat.RequireSelected(layout, "fakecli"); err != nil {
		t.Fatalf("RequireSelected(fakecli) = %v", err)
	}
	want := "claude is not a selected component; select it first, e.g.: sudo codvps install --components claude,codex,cursor,opencode,fakecli"
	if err := cat.RequireSelected(layout, "claude"); err == nil || err.Error() != want {
		t.Fatalf("RequireSelected(claude) = %v, want %q", err, want)
	}
	if got, err := cat.ResolveProviders(layout, ""); err != nil || len(got) != 0 {
		t.Fatalf("a provider without a managed runtime was resolved for update: %v, %v", got, err)
	}

	r := runner.NewFakeRunner()
	r.SetResponse("fakecli", []string{"--version"}, runner.Response{Stdout: "fakecli 1.0\n"})
	reg, err := cat.ReadAt(layout)
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.ComponentState(layout, r, reg, "fakecli", nil); got != "installed-not-configured" {
		t.Fatalf("state before login = %s", got)
	}
	if err := os.MkdirAll(filepath.Join(layout.Home(), ".fakecli"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.Home(), ".fakecli", "token"), []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := cat.Status(layout, r); err != nil {
			t.Fatal(err)
		}
		if err := cat.List(layout); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "fakecli    ready                     install=yes configure=no auth=yes") {
		t.Fatalf("status lacks the added provider's row:\n%s", out)
	}
	if !strings.Contains(out, "coding_clis: fakecli\n") {
		t.Fatalf("list lacks the added provider:\n%s", out)
	}
}
