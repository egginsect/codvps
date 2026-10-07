# Test Parity: Repository Management (Codex membership and isolation removed by)

This document maps every row of `docs/test-parity.md` (the reference
`tests/smoke.sh` registry/membership rows), plus the additional acceptance
criteria in the rework handoff (fail-closed revocation, rejected
checkout cases, and the root-only Codex isolation validator), to a passing
hermetic Go test.

**Owner decision ("let's not do sandbox by ourself") removed the
Codex mount-namespace sandbox.** Rows below marked *removed by* mapped
behavior that no longer exists: the Codex membership manifest, its
fail-closed revocation, and the root-only isolation validator
(`internal/repo/isolation.go`, `isolation_sync.go` and
`codvps internal codex-isolation`). Their tests were deleted with the code.
The repository registry, `repo add/list/remove` and the Claude/Cursor heads
are unchanged; `repo add/remove` no longer touch the Codex daemon. Every row below is `ported` unless marked `deferred`, and
every `deferred` row names the reason (only behavior that genuinely needs a
real systemd instance, a mount namespace, or root).

All tests use `t.TempDir()`/`t.Setenv("HOME", ...)`, local bare Git
repositories as clone sources (no network), and either a fake
`HeadReconciler` (`internal/repo` tests) or `runner.FakeRunner`
(`internal/repo` reconciler-level tests) for every systemd/head effect. No
test in this package invokes a real `git` network remote, `systemctl`,
`sudo`, or root privilege.

## `docs/test-parity.md` rows (`tests/smoke.sh`)

| Reference scenario | Go test(s) | Status |
|---|---|---|
| `test_fixture_add_without_tty` | `TestAdd_ClonesNewRepositoryAndReconcilesHeads` (`internal/repo/repo_test.go`) | ported — `Add` never reads stdin |
| `test_existing_checkout_can_be_reattached_only_to_matching_remote` | `TestAdd_ReattachesExistingCheckoutWithMatchingRemote` (`repo_test.go`), `TestAdd_RejectsMismatchedRemote` (`repo_test.go`) | ported |
| `test_repo_add_auto_attaches_to_the_enabled_claude_head` | `TestAdd_ClonesNewRepositoryAndReconcilesHeads` (`repo_test.go`, asserts `ReconcileClaude` runs on every add and nothing touches Codex) | ported — `Add` always calls the `HeadReconciler`; the Claude half is the real Claude head (, `TestRepoAdd_AutoAttachesToTheEnabledClaudeHead` in `internal/head`), the Codex half is a no-op since (`TestRepoAddRemove_LeaveTheCodexHeadAlone`) |
| `test_repo_add_has_one_noninteractive_membership_mode` (`--no-codex-member`) | n/a | **not ported**: `repo add` takes no flags in this design (see `docs/design/go-core.md`'s command-parity inventory row for `codvps repo add <url>`); there is no Codex membership since |
| `test_repo_add_rolls_back_clean_clone_after_membership_failure` | `TestAdd_RollsBackCleanCloneOnRegistryWriteFailure` (`repo_test.go`) | ported |
| `test_list_and_status` | `TestRenderList_ByteExactFormat` (`repo_test.go`), `TestRenderList_EmptyRegistry` (`repo_test.go`) | ported (table format); the underlying head-state values are queried through `HeadReconciler`, whose full daemon/credential logic is/56 |
| `test_registry_is_the_membership_authority` | `TestList_IgnoresStrayCloneNotInRegistry` (`repo_test.go`) | ported |
| `test_registry_prunes_a_deleted_repository` | `TestList_PrunesVanishedCheckoutWithoutTouchingCodex` (`repo_test.go`) | ported |
| `test_repo_remove_retains_checkout` | `TestRemove_DetachesHeadsAndKeepsCheckout` (`repo_test.go`) | ported |
| `test_repo_membership_rejects_symlinked_physical_repo` | `TestAdd_RejectsSymlinkedCheckout` (`repo_test.go`), `TestAdd_RejectsSymlinkedHomeAncestor` (`repo_test.go`) | ported |
| `test_stray_clone_under_home_is_not_a_repository` | `TestList_IgnoresStrayCloneNotInRegistry` (`repo_test.go`) | ported |
| `test_unregistered_directory_cannot_be_managed` | `TestRemove_NoOpForUnregisteredNameWhenStateIsSafe` (`repo_test.go`) | ported |
| `test_linked_worktree_is_not_a_second_repository` | `TestAdd_RejectsLinkedWorktree` (`repo_test.go`) | ported |
| `test_bad_names_rejected` | `TestAdd_RejectsUnsafeName` (`repo_test.go`), `TestRemove_RejectsUnsafeName` (`repo_test.go`) | ported |
| `test_repo_without_claude_unit_is_not_a_doctor_error` (+) | n/a here | **ported with**: the doctor half is `TestDoctorWarnsAboutAUnitWithoutRepository` (`internal/doctor`) and `17_install_doctor.sh`. The `repo list` half of it (a `-` Claude-head column, `Heads: claude=disabled`) is exercised by `TestRenderList_EmptyRegistry` and the `ClaudeUnitState`/`ClaudeHeadEnabled` cases in `reconcile_test.go` |

## Fake-VPS black-box coverage (`test/fakevps/scenarios/13_repo.sh`)

Each ported row above also runs black-box against the real `codvps repo`
command in the fake VPS, with the Claude head disabled and the product
unit templates installed:

| Reference scenario | `13_repo.sh` function(s) | Status |
|---|---|---|
| `test_fixture_add_without_tty` | `test_add` | ported |
| `test_existing_checkout_can_be_reattached_only_to_matching_remote` | `test_readd_is_idempotent` | ported |
| `test_repo_add_rolls_back_clean_clone_after_membership_failure` | `test_repo_add_rolls_back_clean_clone` | ported |
| `test_list_and_status` | `test_list_exact` | ported |
| `test_registry_is_the_membership_authority` | `test_registry_is_the_membership_authority` | ported |
| `test_registry_prunes_a_deleted_repository` | `test_registry_prunes_a_deleted_repository` | ported |
| `test_repo_remove_retains_checkout` | `test_remove_keeps_checkout` | ported |
| `test_repo_membership_rejects_symlinked_physical_repo` | `test_symlinked_checkout_rejected` | ported |
| `test_stray_clone_under_home_is_not_a_repository` | `test_stray_clone_is_not_a_repository` | ported |
| `test_unregistered_directory_cannot_be_managed` | `test_unregistered_directory_cannot_be_managed` | ported |
| `test_linked_worktree_is_not_a_second_repository` | `test_linked_worktree_is_not_a_second_repository` | ported |
| `test_bad_names_rejected` | `test_bad_name_rejected` | ported |
| `test_repo_add_has_one_noninteractive_membership_mode` | — | not ported — `repo add` takes no flags by design; there is no Codex membership since (listed as `not_ported` in `SKIPPED_SCENARIOS`) |

The reference's versions of the registry-authority, prune, stray-clone and
linked-worktree scenarios also checked the Codex mount policy and doctor;
the mount policy was removed by (see the note at the top).

## Additional acceptance criteria (rework handoff, not literal `smoke.sh` rows)

### `repo remove` (revocation removed by)

| Behavior | Go test |
|---|---|
| Remove detaches the repository from the Claude head and keeps the checkout; it no longer stops the Codex head (*removed by*: fail-closed Codex revocation, its policy-sync failure path and its corrupt-manifest path, formerly `TestRemove_StopsCodexBeforeRevokingAndKeepsCheckout`, `TestRemove_FailsClosedWhenManifestSyncFails`, `TestRepoRemove_CorruptManifestStopsCodexBeforeValidation`) | `TestRemove_DetachesHeadsAndKeepsCheckout` (`repo_test.go`) |
| `repo add`/`repo remove` never touch the Codex head, whether it is off or running (replaces the reconcile/revocation tests) | `TestRepoAddRemove_LeaveTheCodexHeadAlone` (`internal/head`, the real Codex head over a stateful fake system manager) |
| Repeated remove is a true no-op only when the state dir and registry are verified safe (not symlinks, operator-owned, modes 0700/0600); otherwise it falls through to the detach path | `TestRemove_NoOpForUnregisteredNameWhenStateIsSafe` (`repo_test.go`), `TestRemove_FallsThroughWhenStateDirIsUnsafe` (`repo_test.go`), `TestRemove_FallsThroughWhenNameIsRegistered` (`repo_test.go`) |

### Rejected checkout cases (`repo add`)

| Behavior | Go test |
|---|---|
| Unsafe repository name | `TestAdd_RejectsUnsafeName` (`repo_test.go`) |
| Symlinked checkout physical path | `TestAdd_RejectsSymlinkedCheckout` (`repo_test.go`) |
| Symlinked parent (home reached via a symlink) | `TestAdd_RejectsSymlinkedHomeAncestor` (`repo_test.go`) |
| Stray clone not in the registry (invisible, not silently adopted) | `TestList_IgnoresStrayCloneNotInRegistry` (`repo_test.go`) |
| Linked worktree (`.git` is a file / git-common-dir differs) | `TestAdd_RejectsLinkedWorktree` (`repo_test.go`) |
| Checkout whose remote identity does not match the requested URL | `TestAdd_RejectsMismatchedRemote` (`repo_test.go`) |
| Vanished checkouts are pruned without touching the Codex head | `TestList_PrunesVanishedCheckoutWithoutTouchingCodex` (`repo_test.go`, asserts the fake reconciler only receives `DisableClaudeUnit`) |
| A clean new clone is rolled back if the registry write fails | `TestAdd_RollsBackCleanCloneOnRegistryWriteFailure` (`repo_test.go`, injects the failure via a read-only config directory) |
| A corrupt (invalid-name or duplicate) registry fails closed instead of being silently pruned | `TestList_FailsClosedOnCorruptRegistry` (`repo_test.go`) |

### `SystemdHeadReconciler`

*Removed by:* the Codex membership manifest tests
(`TestSyncCodexMembershipManifest_*`, `TestCodexReconcile_TouchesOnlyAWantedHead`,
`TestCodexSyncMembership_SkipsRootSyncWithoutARemoteHome`, `TestCodexStop`) and
the whole root-only isolation validator suite (`TestValidateCodexIsolation_*`,
`TestCodexIsolationSync_*`, `TestRequireWithinHome`).

| Behavior | Go test |
|---|---|
| Claude unit state parsing (`enabled`/`active` from raw `systemctl` output, including a non-zero exit code that still carries a usable state string) | `TestSystemdHeadReconciler_ClaudeUnitStateParsesEnabledAndActive`, `_DisabledAndInactive` (`reconcile_test.go`) |
| A repository name is re-validated before it is used to build a systemd unit string (defense in depth against a corrupted registry) | `TestSystemdHeadReconciler_ClaudeUnitStateRejectsUnsafeName` (`reconcile_test.go`) |
| `DisableClaudeUnit` propagates a genuine `systemctl disable` failure | `TestSystemdHeadReconciler_DisableClaudeUnitPropagatesErrors` (`reconcile_test.go`) |
| Host-wide Claude head toggle reads the flag file | `TestSystemdHeadReconciler_ClaudeHeadEnabledReadsFlagFile` (`reconcile_test.go`) |
| `CodexEnabledState`/`CodexActive` are the Codex head's answers, errors included | `TestSystemdHeadReconciler_DelegatesCodexToTheCodexHead` (`reconcile_test.go`), `TestCodexEnabledAndUnitState` (`internal/head`,) |
| Registry reads are bounded (1 MiB) against operator-writable/corrupted input | `TestList_RejectsOversizedRegistry` (`repo_test.go`) |
| CLI wiring: usage errors, and the removed `codex-isolation` command is rejected | `internal/cli/internal_cmd_test.go` |

## Deferred to the fake-VPS suite (real systemd required)

The mount-namespace and root-gate rows that used to be deferred here were
removed by with the sandbox.

| Behavior | Reason for deferral |
|---|---|
| `repo add`/`repo remove` leave the running Codex daemon alone (real `systemctl`, real unit file) | Landed with: stateful fakes in `internal/head` (`TestRepoAddRemove_LeaveTheCodexHeadAlone`), and the real product unit in the fake-VPS container (`16_head_codex.sh`, `test_shared_daemon`) |
| Codex daemon health via a control-socket listener check (no connection) (not `systemctl is-active`) feeding `CodexActive`/`repo list`'s "running"/"unavailable" | `SystemdHeadReconciler.CodexActive` is the Codex head's socket probe (`TestCodexDaemonProbe`, `TestControlSocketListening`) |
| Full Claude head reconciliation (credential checks, workspace-trust seeding, per-unit health) on `repo add` | Landed with: `SystemdHeadReconciler.ReconcileClaude` delegates to `internal/head.Claude.Reconcile` |
| `codvps doctor` reporting a registered repo with no Claude unit as a WARN, not a failure | Landed with: `TestDoctorWarnsAboutAUnitWithoutRepository` (`internal/doctor`) and `17_install_doctor.sh` |

## Foundation-package changes

None. `internal/paths`, `internal/registry`, `internal/runner`,
`internal/systemd`, and `internal/fsutil` are used as-is.

## Scenario counts

After the `tests/smoke.sh` rows that remain are the registry
rows ported to `internal/repo` and `13_repo.sh`; the membership and isolation
rows are removed. Not ported (superseded by design): 1 (`--no-codex-member`
flag; `repo add` takes no flags in this design).
