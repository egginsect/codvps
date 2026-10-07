#: login test parity

This maps every scenario counted in `docs/test-parity.md`'s
`tests/login-github.sh` table (36 rows) plus the login-related rows of
`tests/smoke.sh` to a Go test in this port, or lists it as deferred with a
reason. All tests below live in `internal/login/*_test.go` and are
hermetic: a temp `HOME` (`t.Setenv`), a temp `PATH` pointing only at fake
`gh`/`ssh`/`ssh-keygen`/`git`/`claude`/`codex` executables that log every
call, and no network access. The real `gh`/`ssh`/`ssh-keygen`/`git`/
`claude`/`codex` binaries are never invoked (see
`internal/login/testutil_test.go`'s `newTestEnv`).

Reference behavior source: the reference implementation at commit
the reference implementation, `tests/login-github.sh` (the file
this document maps line ranges against) and the corresponding rows of
`tests/smoke.sh`. Only the reference's documented, test-visible contract
was ported; the reference CLI script itself was never read, and no
reference text is reproduced here or in the implementation.

## `tests/login-github.sh` (36 scenarios)

| # | Reference scenario (line range) | Go test | Status |
|---|---|---|---|
| 1 | `ensure_ssh_key` non-interactive, no `--key` (L259-264) | `TestSSHKey_NonInteractive_NoChoice_Pending` | ported |
| 2 | `ensure_ssh_key` non-interactive `--key generate` w/o `--passphrase-empty` (L267-275) | `TestSSHKey_Generate_NonInteractive_RequiresPassphraseEmpty` | ported |
| 3 | `ensure_ssh_key` non-interactive `--key generate --passphrase-empty` creates a pair (L278-283) | `TestSSHKey_Generate_NonInteractive_CreatesPairMode0600` | ported |
| 4 | same: private key mode 0600 (L282-283) | `TestSSHKey_Generate_NonInteractive_CreatesPairMode0600` | ported |
| 5 | `ensure_ssh_key` re-run of generate is idempotent (L285-289) | `TestSSHKey_Generate_Idempotent` | ported |
| 6 | `ensure_ssh_key_registered` first run calls `gh ssh-key add` once (L293-298) | `TestRegistration_FirstRun_AddsOnce` | ported |
| 7 | same: records the key against the fake GitHub account state (L298-299) | `TestRegistration_FirstRun_AddsOnce` | ported |
| 8 | `ensure_ssh_key_registered` second run does not add again (L301-304) | `TestRegistration_SecondRun_NoDuplicateAdd` | ported |
| 9 | `ensure_ssh_key_registered` non-interactive without `--yes` is pending (L307-313) | `TestRegistration_NonInteractive_WithoutYes_Pending` | ported |
| 10 | `ensure_ssh_key_registered` stops when `gh ssh-key list` fails (L316-326) | `TestRegistration_ListFailure_Stops` | ported |
| 11 | `ensure_ssh_key_registered` does not add with a scope-limited token (L330-336) | `TestRegistration_MissingScope_NoAdd` | ported |
| 12 | `github_ssh_key_registered` reports false before registration (L339-343) | `TestSSHKeyRegistered_DetectionHelper` | ported |
| 13 | `github_ssh_key_registered` reports true after registration (L344-346) | `TestSSHKeyRegistered_DetectionHelper` | ported |
| 14 | `ensure_known_hosts` first run adds a github.com entry (L350-353) | `TestKnownHosts_FirstRun_AddsEntry` | ported |
| 15 | same: calls `gh api meta` (L354-355) | `TestKnownHosts_FirstRun_AddsEntry` | ported |
| 16 | `ensure_known_hosts` second run does not grow the file (L358-361) | `TestKnownHosts_SecondRun_Idempotent` | ported |
| 17 | `ensure_known_hosts` stops when `gh api meta` is unavailable, with no trust-on-first-use fallback command (L365-371) | `TestKnownHosts_MetaUnavailable_StopsAndUntouched` | ported |
| 18 | same: known_hosts left untouched (L372-374) | `TestKnownHosts_MetaUnavailable_StopsAndUntouched` | ported |
| 19 | `ensure_known_hosts` recognizes pre-existing hashed entries, does not rewrite them (L378-392) | `TestKnownHosts_HashedEntriesRecognized` | ported |
| 20 | `ensure_known_hosts` refuses a conflicting entry, file unchanged (L396-407) | `TestKnownHosts_ConflictingEntry_Refused` | ported |
| 21 | `ensure_ssh_config` does not create the file without a key (L411-416) | `TestSSHConfig_NoKey_NotCreated` | ported |
| 22 | `ensure_ssh_config` adds a `Host github.com` block with a key (L419-423) | `TestSSHConfig_WithKey_AddsHostBlock` | ported |
| 23 | same: adds an `IdentityFile` entry (L424-425) | `TestSSHConfig_WithKey_AddsHostBlock` | ported |
| 24 | `ensure_git_identity` sets both fields from explicit flags (L431-436) | `TestIdentity_ExplicitFlags` | ported |
| 25 | `ensure_git_identity` never overwrites an existing identity, even with different flags (L440-445) | `TestIdentity_NeverOverwritten` | ported |
| 26 | `ensure_git_identity` leaves config untouched with no identity and no flags (L449-454) | `TestIdentity_NoFlags_NoExisting_Pending` | ported |
| 27 | `ensure_git_identity` derives a noreply email from `--identity-name` alone (L457-463) | `TestIdentity_NameOnly_DerivesNoreplyEmail` | ported |
| 28 | `github_verify` succeeds against a successful `ssh -T` (L467-468) | `TestVerify_Success` | ported |
| 29 | `github_verify` fails on a publickey error (L470-476) | `TestVerify_PublickeyFailure` | ported |
| 30 | `github_verify` fails on an account mismatch (L480-484) | `TestVerify_AccountMismatch` | ported |
| 31 | `github_verify` pins the connection to a selected key when no ssh config block routes it (L488-492) | `TestVerify_PinsSelectedKey` | ported |
| 32 | `github_ensure_auth --non-interactive` exits 2 while logged out (L496-501) | `TestEnsureAuth_NonInteractive_LoggedOut_Exit2` | ported |
| 33 | `github_ensure_auth interactive`, no tty, logged out: exits 2, never calls `gh auth login` (L503-511) | `TestGitHub_NoTTY_LoggedOut_StartsDeviceFlow` |  with no tty the device flow now starts (it needs no terminal); only `--non-interactive` exits 2 (`TestGitHub_NoTTY_ExplicitNonInteractive_StillExit2`) |
| 34 | `github_ensure_auth` already logged in succeeds both modes, no relogin (L513-517) | `TestEnsureAuth_AlreadyLoggedIn_NoRelogin` | ported |
| 35 | CLI entry point `--non-interactive`, logged out: exits 2 before any key/identity/known_hosts change (L524-535) | `TestGitHub_CLIEntryPoint_NonInteractive_LoggedOut_Exit2` | ported |
| 36 | same: never calls `gh auth login` (L536-537) | `TestGitHub_CLIEntryPoint_NonInteractive_LoggedOut_Exit2` | ported |

**36/36 ported. Zero deferred** in this file: every scenario in
`tests/login-github.sh` is exercised by calling the same-shaped internal
step function directly (mirroring the reference's own "source the script,
call one function" driver), against fake executables, with a temp `HOME`.
None of these 36 scenarios need systemd, Docker, or a real account.

## Fake-VPS black-box coverage (`test/fakevps/scenarios/12_login_github.sh`)

Every scenario above except the `github_ssh_key_registered` helper rows
also runs black-box: the scenario script runs the whole `codvps login
github` command against fake `gh`/`ssh`/`ssh-keygen`/`ssh-keyscan`/`git`
executables that log their argv, each case under its own scratch HOME, and
asserts the exit code, the step's exact summary line, the files written
and the fake-tool call log. The scenario names are the ones
`test/fakevps/scenarios/SKIPPED_SCENARIOS` used to list.

| # | Scenario | `12_login_github.sh` function(s) | Status |
|---|---|---|---|
| 1 | `test_login_github_key_no_choice_pending` | `test_key_no_choice_is_pending` | ported (black-box and `TestSSHKey_NonInteractive_NoChoice_Pending`) |
| 2 | `test_login_github_key_generate_requires_passphrase_empty` | `test_generate_requires_passphrase_empty` | ported (black-box and `TestSSHKey_Generate_NonInteractive_RequiresPassphraseEmpty`) |
| 3 | `test_login_github_key_generate_creates_pair` | `test_login_github`, `test_key_file` | ported (black-box and `TestSSHKey_Generate_NonInteractive_CreatesPairMode0600`) |
| 4 | `test_login_github_key_mode_0600` | `test_key_file` | ported (black-box and `TestSSHKey_Generate_NonInteractive_CreatesPairMode0600`) |
| 5 | `test_login_github_key_generate_idempotent` | `test_rerun_is_idempotent` | ported (black-box and `TestSSHKey_Generate_Idempotent`) |
| 6 | `test_login_github_register_calls_add_once` | `test_login_github`, `test_rerun_is_idempotent` | ported (black-box and `TestRegistration_FirstRun_AddsOnce`) |
| 7 | `test_login_github_register_records_key` | `test_login_github` | ported (black-box and `TestRegistration_FirstRun_AddsOnce`) |
| 8 | `test_login_github_register_no_duplicate_add` | `test_rerun_is_idempotent` | ported (black-box and `TestRegistration_SecondRun_NoDuplicateAdd`) |
| 9 | `test_login_github_register_pending_without_yes` | `test_register_pending_without_yes` | ported (black-box and `TestRegistration_NonInteractive_WithoutYes_Pending`) |
| 10 | `test_login_github_register_stops_on_list_failure` | `test_register_stops_on_list_failure` | ported (black-box and `TestRegistration_ListFailure_Stops`) |
| 11 | `test_login_github_register_requires_scope` | `test_register_requires_scope` | ported (black-box and `TestRegistration_MissingScope_NoAdd`) |
| 12 | `test_login_github_ssh_key_registered_false_before` | — | ported — Go unit test (`TestSSHKeyRegistered_DetectionHelper`): `github_ssh_key_registered` is an internal helper with no CLI surface of its own |
| 13 | `test_login_github_ssh_key_registered_true_after` | — | ported — Go unit test (`TestSSHKeyRegistered_DetectionHelper`): `github_ssh_key_registered` is an internal helper with no CLI surface of its own |
| 14 | `test_login_github_known_hosts_adds_entry` | `test_known_hosts` | ported (black-box and `TestKnownHosts_FirstRun_AddsEntry`) |
| 15 | `test_login_github_known_hosts_calls_api_meta` | `test_known_hosts` | ported (black-box and `TestKnownHosts_FirstRun_AddsEntry`) |
| 16 | `test_login_github_known_hosts_idempotent` | `test_rerun_is_idempotent` | ported (black-box and `TestKnownHosts_SecondRun_Idempotent`) |
| 17 | `test_login_github_known_hosts_stops_without_meta` | `test_known_hosts_stops_without_meta` | ported (black-box and `TestKnownHosts_MetaUnavailable_StopsAndUntouched`) |
| 18 | `test_login_github_known_hosts_untouched_on_meta_failure` | `test_known_hosts_stops_without_meta` | ported (black-box and `TestKnownHosts_MetaUnavailable_StopsAndUntouched`) |
| 19 | `test_login_github_known_hosts_hashed_entries_recognized` | `test_known_hosts_hashed_entries_recognized` | ported (black-box and `TestKnownHosts_HashedEntriesRecognized`) |
| 20 | `test_login_github_known_hosts_refuses_conflicting_key` | `test_known_hosts_refuses_conflicting_key` | ported (black-box and `TestKnownHosts_ConflictingEntry_Refused`) |
| 21 | `test_login_github_ssh_config_skipped_without_key` | `test_key_no_choice_is_pending`, `test_git_identity_used_by_commit` | ported (black-box and `TestSSHConfig_NoKey_NotCreated`) |
| 22 | `test_login_github_ssh_config_adds_host_block` | `test_ssh_config` | ported (black-box and `TestSSHConfig_WithKey_AddsHostBlock`) |
| 23 | `test_login_github_ssh_config_adds_identityfile` | `test_ssh_config` | ported (black-box and `TestSSHConfig_WithKey_AddsHostBlock`) |
| 24 | `test_login_github_identity_explicit_flags` | `test_login_github` | ported (black-box and `TestIdentity_ExplicitFlags`) |
| 25 | `test_login_github_identity_never_overwritten` | `test_identity_never_overwritten` | ported (black-box and `TestIdentity_NeverOverwritten`) |
| 26 | `test_login_github_identity_pending_without_flags` | `test_identity_pending_without_flags` | ported (black-box and `TestIdentity_NoFlags_NoExisting_Pending`) |
| 27 | `test_login_github_identity_derives_noreply_email` | `test_identity_derives_noreply_email` | ported (black-box and `TestIdentity_NameOnly_DerivesNoreplyEmail`) |
| 28 | `test_login_github_verify_succeeds` | `test_login_github` | ported (black-box and `TestVerify_Success`) |
| 29 | `test_login_github_verify_fails_on_publickey_error` | `test_verify_fails_on_publickey_error` | ported (black-box and `TestVerify_PublickeyFailure`) |
| 30 | `test_login_github_verify_fails_on_account_mismatch` | `test_verify_fails_on_account_mismatch` | ported (black-box and `TestVerify_AccountMismatch`) |
| 31 | `test_login_github_verify_pins_selected_key` | `test_verify_pins_selected_key` | ported (black-box and `TestVerify_PinsSelectedKey`) |
| 32 | `test_login_github_auth_non_interactive_exit2_logged_out` | `test_auth_non_interactive_logged_out` | ported (black-box and `TestEnsureAuth_NonInteractive_LoggedOut_Exit2`) |
| 33 | `test_login_github_auth_interactive_no_tty_exit2` | `test_auth_no_tty_logged_out_runs_device_flow` | changed by (black-box and `TestGitHub_NoTTY_LoggedOut_StartsDeviceFlow`) |
| 34 | `test_login_github_auth_already_logged_in_no_relogin` | `test_auth_already_logged_in_no_relogin` | ported (black-box and `TestEnsureAuth_AlreadyLoggedIn_NoRelogin`) |
| 35 | `test_login_github_cli_non_interactive_exit2_logged_out` | `test_auth_non_interactive_logged_out` | ported (black-box and `TestGitHub_CLIEntryPoint_NonInteractive_LoggedOut_Exit2`) |
| 36 | `test_login_github_cli_never_calls_auth_login` | `test_auth_non_interactive_logged_out` | ported (black-box and `TestGitHub_CLIEntryPoint_NonInteractive_LoggedOut_Exit2`) |

## Login rows of `tests/smoke.sh`

| Reference scenario | Go test | Status |
|---|---|---|
| `test_login_claude_uses_auth_login` | `TestClaudeLogin` (`internal/providers`), `TestRun_CallsTheLoginCommand` | ported (hermetic: asserts the fake `claude` was invoked with exactly `auth login`) |
| `test_login_codex_uses_device_auth` | `TestCodexLogin` (`internal/providers`), `TestRun_ScopesTheCommandEnvironment` | ported (hermetic: asserts exactly `login --device-auth`, and that `CODEX_HOME` is visible to the child process but never leaks into the codvps process's own environment) |
| `test_zero_heritage_path_login_github_no_clone` | `TestGitHub_NoPrivateCloneRegression` | ported as the no-private-bootstrap regression (see below) |
| `test_git_identity_configured` | `12_login_github.sh` (`test_git_identity_used_by_commit`) | ported (black-box): `login github --key skip --identity-name/--identity-email` sets the global identity with the real git under a scratch HOME, and a plain `git commit` afterwards records exactly that author |

## No-private-bootstrap regression (carried from the reference review)

`TestGitHub_NoPrivateCloneRegression` in `internal/login/github_flow_test.go`
is the success-path version this finding asks for:

- a deterministic, fully successful non-interactive auth fixture (fake `gh`
  reports an active, scoped, logged-in account; `--key generate
  --passphrase-empty`; `--identity-name`/`--identity-email` supplied;
  `--yes`);
- an assertion that the flow reaches its end (`GitHub setup summary:` /
  `GitHub setup complete.` in stdout, exit code 0) rather than merely not
  failing;
- the fake `git` in every test's sandbox (`internal/login/testutil_test.go`)
  logs its full argv and exits 99 the moment it is asked to `clone`
  anything, so a reintroduced clone call fails the test loudly instead of
  being silently missed;
- the test also greps the fake git's call log directly for a `clone`
  argument, independent of the exit-code trap.

`TestGitHub_CLIEntryPoint_NonInteractive_LoggedOut_Exit2` additionally
confirms that, before authentication succeeds, no key file, `known_hosts`,
`~/.ssh/config`, or git identity write happens at all — the reference
scenario's specific "logged out, exit 2, no `gh auth login`" case is a
subset of that broader before-any-write guarantee.

## Additional coverage beyond the 36 (binding-contract items not in the numbered list)

The task's binding contract calls out several things `tests/login-github.sh`
doesn't separately enumerate as one of its 36 scenarios. These are covered
by extra tests, listed here for traceability rather than double-counted
above:

| Binding-contract item | Go test |
|---|---|
| Existing key path that does not exist | `TestSSHKey_Existing_Missing` |
| Existing key path that is a symlink (private key) | `TestSSHKey_Existing_SymlinkRejected` |
| Existing key path whose `.pub` is a symlink | `TestSSHKey_Existing_SymlinkedPubRejected` |
| Interactive key generation: cancellation | `TestSSHKey_Interactive_Cancellation` |
| Interactive key generation: empty passphrase choice | `TestSSHKey_Interactive_EmptyPassphrase` |
| Interactive key generation: non-empty passphrase choice, ssh-keygen output kept separate from the selected key path | `TestSSHKey_Interactive_NonEmptyPassphrase` |
| Interactive key generation: an existing pair is reused without prompting | `TestSSHKey_Interactive_ExistingPairReused` |
| Never enroll a key for an unknown (logged-out) account | `TestRegistration_NoActiveAccount_Refused` |
| `ssh -G github.com` used to verify the *effective* identity, not just block presence | `TestSSHConfig_EffectiveIdentityMatchesSelectedKey`, `TestVerify_NoRedundantPin_WhenConfigRoutes` |
| No `insteadOf` ever added | `TestSSHConfig_Idempotent_NoInsteadOf` |
| Multiple `gh` accounts / active-account selection | `TestActiveGitHubAccount_MultipleAccounts` |
| Credential storage reported without reading a token; env override wins | `TestCredentialStorageLabel_EnvOverridesReportedSource`, `TestCredentialStorageLabel_UnknownWhenUnset` |
| Interactive `gh auth login --skip-ssh-key` call shape | `TestEnsureAuth_Interactive_TTY_LoggedOut_LogsIn` |
| Preflight never fails, never prints a secret, reports missing tools by package name | `TestPreflight_NeverFailsNoSecrets`, `TestPreflight_ReportsMissingTool` |
| A pending/skipped key step never runs a failing SSH check nor reports success | `TestGitHub_NonInteractive_PendingKey_Exit2AtSummary`, `TestGitHub_NonInteractive_KeySkip_Succeeds` |
| A hard failure aborts before the summary prints | `TestGitHub_HardFailure_Exit1_NoSummary` |
| `login claude`/`login codex` propagate a failing delegate | `TestRun_PropagatesFailureWithoutAfter`, `TestRun_ScopedCommandPropagatesFailure` |

## Deferred to a real-account / interactive-terminal environment

Only items that genuinely need a real account or a real attached terminal
are deferred, each with a reason; none of the 36 required scenarios are
among them:

| Item | Reason deferred |
|---|---|
| The real interactive device-auth prompt sequence of `gh auth login` (byte-for-byte terminal I/O) | Needs a real pseudo-terminal and a real GitHub account; this port instead drives the interactive code path with an injected `IsTTY`/`Stdin` (see `TestEnsureAuth_Interactive_TTY_LoggedOut_LogsIn`), which exercises the same branch without a real tty. |
| `gh auth refresh` consent flow (re-authorizing scopes) | Requires an interactive OAuth consent screen against a real GitHub account; codvps only detects the missing-scope state (`TestRegistration_MissingScope_NoAdd`) and tells the operator to run it. |
| End-to-end SSH access to the real `github.com` host | Requires real network egress and a real registered key; `verify`'s logic is fully covered against the fake `ssh` (scenarios 28-31). |
| systemd/Docker-based `tests/smoke.sh` scenarios not listed above (install, head, doctor, etc.) | Out of scope for; owned by/55/56/57 per `docs/test-parity.md`. |

## Foundation-package changes

None. `internal/login` builds entirely on `internal/runner` (unmodified)
and stdlib. It does not use `internal/paths` or
`internal/fsutil`'s directory-creation helpers beyond `fsutil.AtomicWrite`,
which was already exported and used as-is for `known_hosts` and
`~/.ssh/config` writes.
