# Test Parity: Components 

**Date:** 2026-09-28

This document maps the 32 scenarios from the reference implementation's
`tests/components.sh` to the corresponding Go tests in
`internal/components/components_test.go`.

This revision replaces an earlier version of this document that was
rejected on independent review: several rows named tests that never
asserted the behavior they claimed to cover (`_, _ = layout, regPath`-style
bodies), the registry schema did not match the reference's
`{"version":1,"coding_clis":[...],"switch":"..."}` shape, the capability
table was missing `native_remote` and had several wrong values, and
`components status`/`components list` did not match the reference's output
format. All of that has been rewritten; every row below names a test that
actually calls the function under test and asserts an exact result.

---

## Scenario Counts

**tests/components.sh (32 scenarios):** 32 ported with a Go unit test that
fails without the behavior it asserts. Sourcing the library without `jq`
has no Go equivalent to source, so its contract (registry handling needs
no external tool) is `TestRegistryHandlingNeedsNoExternalTools`, which runs
the registry path with an empty `PATH`. The scenarios with a CLI surface
also run black-box (see "Fake-VPS black-box coverage" below).

**Total: 32 ported core.**

---

## Reference Scenarios → Go Tests (components.sh)

| # | Reference Scenario | Behavior | Go Test | Status |
|---|---|---|---|---|
| 1 | `components_validate_clis_csv 'claude,codex'` (L45-46) | Valid two-CLI selection round-trips unchanged | `TestValidateCLIsBoth` | ported |
| 2 | `components_validate_clis_csv 'claude'` (L48-49) | Valid single-CLI selection round-trips | `TestValidateCLIsSingle` | ported |
| 3 | `components_validate_clis_csv 'none'` (L51-52) | `none` validates to an empty selection | `TestValidateCLIsNone` | ported |
| 4 | `components_validate_clis_csv ''` (L54-55) | Empty string validates to an empty selection | `TestValidateCLIsEmpty` | ported |
| 5 | `components_validate_clis_csv 'claude,claude'` (L57-58) | Duplicates deduplicated | `TestValidateCLIsDedupe` | ported |
| 6 | `components_validate_clis_csv 'claude,cursor'` (L60-64) | Unknown CLI dies naming "unknown coding CLI"; exact reference wording asserted | `TestValidateCLIsUnknownDies` | ported |
| 7 | `components_validate_switch 'none'` (L68-69) | Switch value `none` round-trips | `TestValidateSwitchNone` | ported |
| 8 | `components_validate_switch` of an unrecognized name (L71-72) | An unrecognized switch name is rejected | `TestValidateSwitchUnknownDies` | ported |
| 9 | `components_validate_switch ''` (L74-75) | Empty switch defaults to `none` | `TestValidateSwitchDefaultNone` | ported |
| 10 | `components_validate_switch 'cc-switch'` (L77-81) | `cc-switch` dies pointing at | `TestValidateSwitchCCSwitchPlanned` | ported |
| 11 | `components_validate_switch 'bogus'` (L83-87) | Unknown switch dies naming "unknown --switch value"; exact reference wording asserted | `TestValidateSwitchUnknownDies` | ported |
| 12 | `components_write 'claude,codex' 'none'` (L91-94) | Writing the registry creates the file at mode 0644 | `TestWriteCreatesRegistryAtMode0644` | ported (ownership: see note below) |
| 13 | `component_selected`/`switch_selected` after write (L96-98) | Selection reflects the written registry | `TestRegistrySelectedReflectsRewrite` | ported |
| 14 | rewrite to `'claude'`/`'none'` (L100-107) | Rewriting updates every changed and unchanged entry | `TestRegistrySelectedReflectsRewrite` | ported |
| 15 | `component_selected` on missing registry (L111-114) | Missing registry = nothing selected, not an error | `TestMissingRegistryNothingSelected` | ported |
| 16 | `components_selected_switch` on missing registry (L115-116) | Missing registry's switch defaults to `none` | `TestMissingRegistrySwitchDefaultsNone` | ported |
| 17 | `components_read_json` on a corrupt registry (L120-124) | Corrupt registry is a loud read failure, not a silent default | `TestCorruptRegistryLoudFailure`, `TestReadRejectsWrongVersion`, `TestReadRejectsUnknownCLIInCodingClis`, `TestReadRejectsInvalidSwitch`, `TestReadRejectsNonArrayCodingClis` | ported |
| 18 | `component_capability claude install` (L128-129) | `claude install` = `yes` | `TestCapabilityTableMatchesReference` | ported |
| 19 | `component_capability` of an unrecognized name (L131-132) | unrecognized component/action = `unknown` | `TestCapabilityUnknownComponentOrAction` | ported |
| 20 | `component_capability cc-switch install` (L134-135) | Every `cc-switch` capability = `planned` | `TestCapabilityTableMatchesReference` | ported |
| 21 | `component_capability bogus install` (L137-138) | Unknown component/action = `unknown` | `TestCapabilityUnknownComponentOrAction` | ported |
| 22 | sourcing without `jq` (L188-195) | Never shells out to `jq` | `TestRegistryHandlingNeedsNoExternalTools` | ported — Go unit test: Go has no module to source, so the test runs registry read/write, validation, gating and the fresh-host check with an empty `PATH` |
| 23 | `components_fresh_host_needs_selection` fresh/no-flags/no-tty (L197-227) | Fresh, flagless, TTY-less host with no installed binary needs a selection | `TestFreshHostNeedsSelectionTrueWhenFreshNoFlagsNoTTYNoBinary` | ported |
| 23a | (implicit in the reference's own-binary-path parameter) | An already-installed binary means no selection is needed even with no flags/TTY | `TestFreshHostNeedsSelectionFalseWhenBinaryInstalled` | ported |
| 24 | `components_fresh_host_needs_selection` with `--components` given | `--components` alone means no selection needed | `TestFreshHostNeedsSelectionFalseWhenComponentsFlagGiven` | ported |
| 24a | (implicit: `--switch` is checked independently of `--components`) | `--switch` alone (no `--components`) also means no selection needed | `TestFreshHostNeedsSelectionFalseWhenSwitchFlagGiven` | ported |
| 24b | (implicit: `[[ -t 0 ]] && [[ -t 1 ]]`) | A usable TTY means no selection needed | `TestFreshHostNeedsSelectionFalseWhenTTYAvailable` | ported |
| 25 | `components_fresh_host_needs_selection` with an existing registry | Existing registry means no selection needed | `TestFreshHostNeedsSelectionFalseWhenRegistryExists` | ported |
| 25a | install.sh's die message around the fresh-host check | Exact lead sentence and example command | `TestFreshHostSelectionMessageMatchesReference` | ported |
| 26 | `runtime_resolve_providers … codex` on a claude-only host (L255-267) | `codvps update codex` refuses with an actionable `--components` message; exact reference wording asserted | `TestResolveProvidersCodexUnselectedFails` | ported |
| 27 | `runtime_resolve_providers` of an unrecognized name (L269-278) | An unrecognized provider name is a distinct unknown-provider error | `TestResolveProvidersUnknownProviderFails` | ported |
| 28 | `runtime_resolve_providers … claude` on a claude-only host (L280-285) | `codvps update claude` still resolves | `TestResolveProvidersClaudeOnClaudeOnlyHostResolves` | ported |
| 28a | (implicit: `all`/empty selects every selected provider) | `codvps update` / `update all` resolves every selected provider in catalog order | `TestResolveProvidersAllReturnsSelectedInReferenceOrder` | ported |
| 28b | (implicit: an unrecognized word dies, distinct from "unselected") | An unknown provider name is a distinct "unknown provider" error naming the updatable ones | `TestResolveProvidersUnknownProviderFails` | ported |
| 29 | `login_codex` on a claude-only host (L287-293) | `codvps login codex` refuses on a claude-only host with the reference's exact gating text | `TestRequireSelectedMessageMatchesReference` | ported (gating logic and exact message); `codvps login codex` calls `RequireSelected` before running codex (, `TestLoginCodexRefusedWhenCodexUnselected`, `16_head_codex.sh`) |
| 30 | `login_claude` with nothing selected (L295-303) | `codvps login claude` refuses when claude is not selected, exact gating text | `TestRequireSelectedMessageMatchesReference` | ported (gating logic and exact message); `codvps login claude` calls `RequireSelected` before `claude auth login` , exercised black-box by `test/fakevps/scenarios/15_head_claude.sh` |
| 31 | `runtime_report codex` on a claude-only host (L306-309) | Unselected provider reports `not-selected` | `TestGetReportUnselectedIsNotSelectedNeverDrift` | ported |
| 32 | `runtime_report codex` on a claude-only host (L310-312) | Unselected provider is never `DRIFT`/`unmanaged` | `TestGetReportUnselectedIsNotSelectedNeverDrift` | ported |

## Fake-VPS black-box coverage

The scenario names below are the ones `test/fakevps/scenarios/SKIPPED_SCENARIOS`
used to list (plus the three `11_components.sh` already covered). A
scenario with a CLI surface runs black-box: `11_components.sh` drives
`components list|status` over a root-written registry and runs
`sudo codvps install` with each invalid `--components`/`--switch` value
(install refuses before its first host mutation, and the registry is
asserted absent afterwards); `17_install_doctor.sh` covers the registry
write (exact JSON, `root`-owned 0644) and the fresh-host selection gate. The
remaining rows test a pure function with no CLI surface of its own and are
Go unit tests only.

| Scenario | Script (function) | Go test | Status |
|---|---|---|---|
| `test_components_validate_clis_both` | — | `TestValidateCLIsBoth` | ported — Go unit test (pure validator return value; install only ever passes the result on) |
| `test_components_validate_clis_single` | — | `TestValidateCLIsSingle` | ported — Go unit test (pure validator return value; install only ever passes the result on) |
| `test_components_validate_clis_none` | — | `TestValidateCLIsNone` | ported — Go unit test (pure validator return value; install only ever passes the result on) |
| `test_components_validate_clis_empty` | — | `TestValidateCLIsEmpty` | ported — Go unit test (pure validator return value; install only ever passes the result on) |
| `test_components_validate_clis_dedupe` | — | `TestValidateCLIsDedupe` | ported — Go unit test (pure validator return value; install only ever passes the result on) |
| `test_components_validate_clis_unknown_dies` | `11_components.sh` (`test_install_rejects_invalid_selection`) | `TestValidateCLIsUnknownDies` | ported |
| `test_components_validate_switch_none` | — | `TestValidateSwitchNone` | ported — Go unit test (pure validator return value; install only ever passes the result on) |
| `test_components_validate_switch` of an unrecognized name | — | `TestValidateSwitchUnknownDies` | ported — Go unit test (pure validator return value; install only ever passes the result on) |
| `test_components_validate_switch_default_none` | — | `TestValidateSwitchDefaultNone` | ported — Go unit test (pure validator return value; install only ever passes the result on) |
| `test_components_validate_switch_cc_switch_reserved` | `11_components.sh` (`test_install_rejects_invalid_selection`) | `TestValidateSwitchCCSwitchPlanned` | ported |
| `test_components_validate_switch_unknown_dies` | `11_components.sh` (`test_install_rejects_invalid_selection`) | `TestValidateSwitchUnknownDies` | ported |
| `test_components_write_creates_registry_mode` | `17_install_doctor.sh` (`test_install`) | `TestWriteCreatesRegistryAtMode0644` | ported |
| `test_components_selected_reflects_write` | `11_components.sh` (`test_full_selection`) | `TestRegistrySelectedReflectsRewrite` | ported |
| `test_components_selected_reflects_rewrite` | `11_components.sh` (`test_claude_only_selection`) | `TestRegistrySelectedReflectsRewrite` | ported |
| `test_components_missing_registry_nothing_selected` | `11_components.sh` (`test_missing_registry`) | `TestMissingRegistryNothingSelected` | ported |
| `test_components_missing_registry_switch_none` | `11_components.sh` (`test_missing_registry`) | `TestMissingRegistrySwitchDefaultsNone` | ported |
| `test_components_corrupt_registry_loud_failure` | `11_components.sh` (`test_corrupt_registry`) | `TestCorruptRegistryLoudFailure` | ported |
| `test_components_capability_claude_install_yes` | — | `TestCapabilityTableMatchesReference` | ported — Go unit test (capability-table lookup; the same values are the exact status rows 11_components.sh test_missing_registry asserts) |
| `test_components_capability` of an unrecognized name | — | `TestCapabilityUnknownComponentOrAction` | ported — Go unit test (unknown names have no CLI surface) |
| `test_components_capability_cc_switch_planned` | — | `TestCapabilityTableMatchesReference` | ported — Go unit test (capability-table lookup; the same values are the exact status rows 11_components.sh test_missing_registry asserts) |
| `test_components_capability_unknown` | — | `TestCapabilityUnknownComponentOrAction` | ported — Go unit test (unknown names have no CLI surface) |
| `test_components_source_without_jq` | — | `TestRegistryHandlingNeedsNoExternalTools` | ported — Go unit test (Go has no module to source; the contract is that registry handling needs no external tool) |
| `test_components_fresh_host_needs_selection_true` | `17_install_doctor.sh` (`test_install_needs_a_selection_on_a_fresh_host`) | `TestFreshHostNeedsSelectionTrueWhenFreshNoFlagsNoTTYNoBinary`, `TestRegistryHandlingNeedsNoExternalTools` | ported |
| `test_components_fresh_host_flags_given_no_selection_needed` | `17_install_doctor.sh` (`test_install`) | `TestFreshHostNeedsSelectionFalseWhenComponentsFlagGiven`, `TestRegistryHandlingNeedsNoExternalTools` | ported |
| `test_components_fresh_host_existing_registry_no_selection_needed` | `17_install_doctor.sh` (`test_install_is_idempotent`) | `TestFreshHostNeedsSelectionFalseWhenRegistryExists`, `TestRegistryHandlingNeedsNoExternalTools` | ported |

**Ownership note (row 12):** the reference `chmod`s the registry 0644 and
best-effort `chown 0:0`s it (`chown ... || true`). This port's `Write`
matches the 0644 mode (asserted). It cannot assert the root:root ownership
in an unprivileged test process — the same reason the reference's own
`chown` is best-effort — so ownership is left to whatever runs `Write` as
root (`codvps install`,, which writes it as root), exactly as the reference leaves it to whichever
of its own callers runs as root.

---

## Component State Coverage

All 6 `component_state` values now have a direct test that drives the real
probe path (a `runner.FakeRunner` standing in for the reference's
`runtime_unit_binary`, plus a real credential file on a temp `$HOME`):

| State | Test |
|---|---|
| `not-selected` | `TestComponentStateNotSelected` |
| `selected-not-installed` | `TestComponentStateSelectedNotInstalled` |
| `installed-not-configured` | `TestComponentStateInstalledNotConfigured`, `TestComponentStateClaudeRejectsSymlinkedCredentials`, `TestComponentStateReadyForCodexRequiresNonEmptyAuth` (empty-auth.json case) |
| `ready` | `TestComponentStateReadyForClaude`, `TestComponentStateReadyForCodexRequiresNonEmptyAuth` (non-empty-auth.json case) |
| `failed` | `TestComponentStateFailedOnBrokenBinary` |
| `unsupported` | `TestComponentStateCCSwitchAlwaysUnsupported` |

**Credential checks:** `configured` for claude and codex reads
`$HOME/.claude/.credentials.json` and `$HOME/.codex-remote/auth.json`,
the same locations as the reference (`claude_credential_source_state` /
`has_codex_cred`), with the same existence, regular-file and non-empty
semantics. `~/.codex-remote` is the Codex Remote home that `codvps login
codex` writes (docs/design/go-core.md).

**Adaptation note:** Likewise, `probeInstalled` stands in for the reference's
`runtime_unit_binary`, which resolves a pinned, staged binary path under
the reference's own runtime root. codvps's runtime pin now exists (,
`internal/runtimes.Resolver`), but `components status` still probes the
CLI's expected `PATH` entry point (`claude`, `codex`) through
`runner.Runner`; switching its installed/failed states to the pin's
resolution is a follow-up, and the pin-aware per-provider view is the
runtime table in `codvps status`.

---

## `components status` / `components list` Output

Both now match the reference's exact format:

- `components list` prints `coding_clis: <csv|none>` then `switch: <value>`
  (the reference's `components_list`), asserted by `TestListOutputFormat`
  and `TestListOutputFormatNothingSelected`.
- `components status` prints the reference's fixed header
  (`COMPONENT`/`STATE`/`CAPABILITIES(...)` column layout) and one row per
  component in the reference's exact iteration order — `claude`, `codex`,
  `cursor`, `opencode`, `cc-switch` — each row carrying every capability including
  `native_remote`, asserted line-for-line by
  `TestStatusIteratesInReferenceOrderAndFormat`.
- `components help` / `--help` / `-h` print the reference's
  `components_usage` text renamed only from the reference implementation's
  product name to codvps, asserted byte-for-byte by
  `TestUsageMatchesReferenceTextRenamedOnly`.

---

## Gating Message Rendering (`codvps install`)

The reference builds every gating message with `$SCRIPT_DIR`, the directory
`install.sh` was invoked from — available because the reference is sourced
from a repository checkout before any installed state exists. codvps ships
as a single static binary whose root-run install step is `codvps install`
, so the messages name `sudo codvps install --components …` /
`--switch …` through the single `InstallCommand` constant in
`internal/components/components.go`. Every other word of the message is
unchanged from the reference, asserted by
`TestRequireSelectedMessageMatchesReference`,
`TestResolveProvidersCodexUnselectedFails`, and
`TestFreshHostSelectionMessageMatchesReference`.

---

## Hermetic Testing Guarantee

Every test in `internal/components/components_test.go` builds its
`paths.Layout` through `newTestLayout(t)`, which sets `HOME` to a fresh
`t.TempDir()` and calls `paths.Layout.WithSystemRoot(t.TempDir())` so `etc`,
`opt`, `run`, and `libexec` paths never resolve under the real `/etc`.
`TestEtcPathStaysUnderSystemRoot` is a standing guard: it fails if
`EtcPath()` (or `RegistryPath()`) does not start with the temp root, so a
future regression that drops `WithSystemRoot` from `newTestLayout` is
caught immediately rather than silently reading or writing the real
`/etc/codvps`.

---

## Exported Helpers

- **`RequireSelected(layout, component) error`** — the reference's
  `login_claude`/`login_codex` gating check and exact message text.
- **`ResolveProviders(layout, providerName) ([]string, error)`** — the
  reference's `runtime_resolve_providers`; resolves `""`/`"all"` to every
  selected provider in reference order, a specific selected provider to
  itself, refuses a specific unselected provider with the reference's
  `--components`/`--switch` wording, and rejects an unrecognized name.
- **`ValidateSelection(clisCSV, switchVal) error`**, **`ValidateCLIs`**,
  **`ValidateSwitch`** — for `codvps install --components`/`--switch` .
- **`FreshHostNeedsSelection(layout, binaryInstalled, hasComponentsFlag,
  hasSwitchFlag, hasTTY) (bool, error)`** and
  **`FreshHostSelectionMessage() string`** — `codvps install`'s 
  provisioning gate.
- **`ComponentState`**, **`GetReport`** — status reporting: unselected is
  always `not-selected`, never `DRIFT`/`unmanaged`.
- **`Usage() string`** — `components help`/`--help`/`-h` text.

---

## Acceptance Criteria

- All 32 `tests/components.sh` scenarios are ported or explicitly marked
  not applicable, each naming a Go test that fails without the behavior.
- The registry schema, strict corrupt-read validation, and a byte-level
  round trip of the reference's exact JSON match `TestRegistryRoundTripsExactReferenceBytes`, `TestReadRejectsWrongVersion`,
  `TestReadRejectsUnknownCLIInCodingClis`, `TestReadRejectsInvalidSwitch`,
  `TestReadRejectsNonArrayCodingClis`.
- The capability table (including `native_remote` and `cc-switch`) is
  pinned in one table test transcribed from the reference:
  `TestCapabilityTableMatchesReference`.
- `components status` iterates `claude, codex, cursor, opencode, cc-switch`,
  emits `unsupported` for `cc-switch`, and computes every other state from
  real probes: `TestStatusIteratesInReferenceOrderAndFormat` and the
  Component State Coverage table above.
- No test resolves under the real `/etc`:
  `TestEtcPathStaysUnderSystemRoot` guards it.
- Every test asserts an exact result; no assertion-free test remains.
- Gating messages match the reference text exactly, adapted only by
  product name and the documented `InstallScriptPath` rendering.
- `FreshHostNeedsSelection` mirrors the reference's four preconditions
  (no registry, no installed binary, no flags, no TTY), including the
  binary-installed and switch-only cases, and its exact die message.
- `components help`/`--help`/`-h` print the reference's usage text,
  renamed only.
- All quality gates pass: `go vet`, `go test -count=1 ./...`, `gofmt -l .`
  (empty), `golangci-lint run ./...` (0 issues), and an
  arm64 cross-build.
- DCO-signed commits.
