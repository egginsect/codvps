package login

import (
	"errors"
	"fmt"
	"os/exec"
	"os/user"
	"strings"
)

// GitHub runs `codvps login github`'s guided flow: preflight, gh
// authentication, SSH key selection, key registration, known_hosts,
// SSH config, git identity and a final SSH verification. It returns the
// process exit code: 0 when every step is ok/skipped, 1 on a hard failure,
// and 2 when the flow is incomplete and the effective non-interactive mode
// (--non-interactive, or no real terminal attached) cannot proceed further
// without an operator decision.
func GitHub(ctx *Context, nonInteractiveFlag bool, keyChoice string, passphraseEmpty bool, identityName, identityEmail string, yes bool, deferOnly bool) (int, error) {
	ctx.printf("%s", Preflight(ctx))

	if deferOnly {
		return 0, nil
	}

	ctx.AssumeYes = ctx.AssumeYes || yes

	// Without a terminal the flow still runs: GitHub's device flow needs
	// none, and each prompt takes its default answer (reported as it
	// happens). Only --non-interactive makes a step stop and report
	// pending; the key registration and the exit code also treat "no
	// terminal" as non-interactive, since nobody could confirm there.
	nonInteractive := nonInteractiveFlag || !ctx.IsTTY()

	if err := ensureGH(ctx); err != nil {
		ctx.errPrintf("Error: %s\n", err)
		return 1, err
	}

	authStatus, authDetail, err := ensureAuth(ctx, nonInteractiveFlag)
	if err != nil {
		ctx.errPrintf("Error: %s\n", err)
		if errors.Is(err, ErrAuthRequired) {
			return 2, err
		}
		return 1, err
	}

	var results []StepResult
	results = append(results, StepResult{"gh_account", authStatus, authDetail})

	selectedKey, keyStatus, keyDetail, err := ensureSSHKey(ctx, nonInteractiveFlag, keyChoice, passphraseEmpty)
	if err != nil {
		ctx.errPrintf("Error: %s\n", err)
		return 1, err
	}
	results = append(results, StepResult{"key", keyStatus, keyDetail})

	regStatus, regDetail := StatusSkipped, "no key selected"
	if selectedKey != "" {
		regStatus, regDetail, err = ensureSSHKeyRegistered(ctx, nonInteractive, selectedKey, yes)
		if err != nil {
			ctx.errPrintf("Error: %s\n", err)
			return 1, err
		}
	}
	results = append(results, StepResult{"key_registration", regStatus, regDetail})

	khStatus, khDetail, err := ensureKnownHosts(ctx)
	if err != nil {
		ctx.errPrintf("Error: %s\n", err)
		return 1, err
	}
	results = append(results, StepResult{"known_hosts", khStatus, khDetail})

	cfgStatus, cfgDetail, err := ensureSSHConfig(ctx, selectedKey)
	if err != nil {
		ctx.errPrintf("Error: %s\n", err)
		return 1, err
	}
	results = append(results, StepResult{"ssh_config", cfgStatus, cfgDetail})

	idStatus, idDetail, err := ensureGitIdentity(ctx, identityName, identityEmail)
	if err != nil {
		ctx.errPrintf("Error: %s\n", err)
		return 1, err
	}
	results = append(results, StepResult{"identity", idStatus, idDetail})

	// A key step that ended pending or skipped must never be followed by a
	// (necessarily failing, or meaningless) SSH check, and must never let
	// the run report "Ready".
	var verifyStatus Status
	var verifyDetail string
	if keyStatus == StatusPending || keyStatus == StatusSkipped {
		verifyStatus, verifyDetail = StatusSkipped, "key setup is "+string(keyStatus)
	} else {
		verifyStatus, verifyDetail, err = verify(ctx, selectedKey)
		if err != nil {
			ctx.errPrintf("Error: %s\n", err)
			return 1, err
		}
	}
	results = append(results, StepResult{"ssh_auth", verifyStatus, verifyDetail})

	printSummary(ctx, results)
	return overallExitCode(results, nonInteractive), nil
}

func overallExitCode(results []StepResult, nonInteractive bool) int {
	hasFailed, hasPending := false, false
	for _, r := range results {
		switch r.Status {
		case StatusFailed:
			hasFailed = true
		case StatusPending:
			hasPending = true
		case StatusOK, StatusSkipped:
		}
	}
	switch {
	case hasFailed:
		return 1
	case hasPending && nonInteractive:
		return 2
	default:
		return 0
	}
}

func printSummary(ctx *Context, results []StepResult) {
	ctx.printf("\nGitHub setup summary:\n")
	hasFailed, hasPending := false, false
	for _, r := range results {
		ctx.printf("  %-18s %-8s %s\n", r.Name, r.Status, r.Detail)
		switch r.Status {
		case StatusFailed:
			hasFailed = true
		case StatusPending:
			hasPending = true
		case StatusOK, StatusSkipped:
		}
	}
	switch {
	case hasFailed:
		ctx.printf("GitHub setup FAILED.\n")
	case hasPending:
		ctx.printf("GitHub setup incomplete; rerun with the missing flags, or pass --defer to only recheck.\n")
	default:
		ctx.printf("GitHub setup complete.\n")
	}
}

// Preflight is the read-only report behind `--defer`: it is also printed at
// the start of every other run. It never prints a secret: no token is ever
// read, only whether one is configured and where it is stored.
func Preflight(ctx *Context) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "Preflight:\n")
	_, _ = fmt.Fprintf(&b, "  operator: %s\n", operatorName())

	for _, tool := range []struct{ name, pkg string }{
		{"git", "git"},
		{"ssh", "openssh-client"},
		{"ssh-keygen", "openssh-client"},
		{"gh", "gh"},
	} {
		if _, err := exec.LookPath(tool.name); err != nil {
			_, _ = fmt.Fprintf(&b, "  %-10s missing (install: %s)\n", tool.name, tool.pkg)
		} else {
			_, _ = fmt.Fprintf(&b, "  %-10s found\n", tool.name)
		}
	}

	accounts, err := ghAuthStatus(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(&b, "  gh account: unknown (%s)\n", err)
	} else if account, ok := activeGitHubAccount(accounts); ok {
		_, _ = fmt.Fprintf(&b, "  gh account: %s\n", account.Login)
		_, _ = fmt.Fprintf(&b, "  gh scopes: %s\n", strings.Join(account.Scopes, ", "))
		_, _ = fmt.Fprintf(&b, "  credential storage: %s\n", credentialStorageLabel(account))
	} else {
		_, _ = fmt.Fprintf(&b, "  gh account: none (not logged in)\n")
	}

	name, nameSet, _ := gitConfigGet(ctx, "user.name")
	email, emailSet, _ := gitConfigGet(ctx, "user.email")
	switch {
	case nameSet && emailSet:
		_, _ = fmt.Fprintf(&b, "  git identity: %s <%s>\n", name, email)
	default:
		_, _ = fmt.Fprintf(&b, "  git identity: not set\n")
	}

	keys := candidateKeys(ctx)
	if len(keys) == 0 {
		_, _ = fmt.Fprintf(&b, "  candidate keys: none\n")
	} else {
		_, _ = fmt.Fprintf(&b, "  candidate keys:\n")
		for _, k := range keys {
			fp, ferr := keyFingerprint(ctx, k+".pub")
			if ferr != nil {
				fp = "unknown"
			}
			_, _ = fmt.Fprintf(&b, "    %s (%s)\n", k, fp)
		}
	}
	return b.String()
}

func operatorName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "unknown"
}
