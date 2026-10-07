package login

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/egginsect/codvps/internal/ghcli"
)

// errSudoNeedsTerminal is returned when installing gh needs root, sudo would
// prompt for a password, and there is no terminal to answer it.
var errSudoNeedsTerminal = errors.New("gh is missing or too old for codvps, and installing the current gh needs root: sudo would prompt for a password but there is no terminal; run sudo codvps install (installs the current gh)")

// ensureGH is the first step of the guided flow: make sure the host has a
// gh that supports `gh auth status --json`, installing or upgrading it on
// demand the way host provisioning does (internal/ghcli). A gh that merely
// fails for another reason is left to the auth step to report.
func ensureGH(ctx *Context) error {
	state := ghState(ctx)
	if state == ghcli.Usable {
		return nil
	}
	what := "missing"
	if state == ghcli.TooOld {
		what = "too old for codvps"
	}
	ctx.printf("gh is %s: installing the current gh from %s (needs sudo).\n", what, ghcli.PackagesURL)

	sudo, err := ctx.sudoPrefix()
	if err != nil {
		return err
	}
	if err := ghcli.Install(&ghInstaller{ctx: ctx, sudo: sudo}); err != nil {
		return fmt.Errorf("installing the current gh failed: %w (or run sudo codvps install)", err)
	}
	if after := ghState(ctx); after != ghcli.Usable {
		return errors.New("the current gh was installed but the gh on PATH is still missing or too old; check which gh is first on PATH, or run sudo codvps install")
	}
	ctx.printf("gh is now installed.\n")
	return nil
}

func ghState(ctx *Context) ghcli.State {
	_, stderr, exitCode, err := ctx.Runner.Run("gh", ghcli.ProbeArgs...)
	return ghcli.Classify(stderr, exitCode, err)
}

// sudoPrefix is the command prefix that runs a command as root: none for
// root itself, sudo otherwise. Without a terminal, sudo must not need a
// password (`sudo -n true`), since nobody could type it.
func (ctx *Context) sudoPrefix() ([]string, error) {
	if ctx.IsRoot != nil && ctx.IsRoot() {
		return nil, nil
	}
	if _, _, code, err := ctx.Runner.Run("sudo", "-n", "true"); code == 0 && err == nil {
		return []string{"sudo"}, nil
	}
	if !ctx.IsTTY() {
		return nil, errSudoNeedsTerminal
	}
	return []string{"sudo"}, nil
}

// ghInstaller is the login Context as a ghcli.Host: privileged steps go
// through sudo, and every command is shown before it runs.
type ghInstaller struct {
	ctx  *Context
	sudo []string
}

func (g *ghInstaller) Printf(format string, args ...any) { g.ctx.printf(format, args...) }

func (g *ghInstaller) run(stdin *bytes.Reader, name string, args ...string) error {
	ctx := g.ctx
	ctx.printf("+ %s\n", strings.Join(append([]string{name}, args...), " "))
	if stdin == nil {
		stdin = bytes.NewReader(nil)
	}
	code, err := ctx.Runner.RunWithIO(name, args, stdin, ctx.out(), ctx.errOut())
	if code != 0 || err != nil {
		return runFailure(name, code, err)
	}
	return nil
}

func (g *ghInstaller) Run(name string, args ...string) error { return g.run(nil, name, args...) }

func (g *ghInstaller) Root(name string, args ...string) error {
	if len(g.sudo) == 0 {
		return g.run(nil, name, args...)
	}
	return g.run(nil, g.sudo[0], append(g.sudo[1:], append([]string{name}, args...)...)...)
}

func (g *ghInstaller) Output(name string, args ...string) (string, error) {
	stdout, stderr, code, err := g.ctx.Runner.Run(name, args...)
	if code != 0 || err != nil {
		return "", fmt.Errorf("%s failed: exit %d: %s", name, code, strings.TrimSpace(stderr))
	}
	return stdout, nil
}

func (g *ghInstaller) Exists(path string) bool {
	_, err := os.Lstat(g.ctx.SysRoot + path)
	return err == nil
}

// Write installs data at path as a root-owned file (creating its
// directory), unless that exact content is already there.
func (g *ghInstaller) Write(path string, data []byte, mode os.FileMode) error {
	if old, err := os.ReadFile(g.ctx.SysRoot + path); err == nil {
		if bytes.Equal(old, data) {
			return nil
		}
		g.ctx.printf("replacing %s: it differs from what codvps provisions there\n", path)
	} else {
		g.ctx.printf("writing %s\n", path)
	}
	args := []string{"install", "-D", "-m", fmt.Sprintf("%04o", mode.Perm()), "/dev/stdin", path}
	if len(g.sudo) > 0 {
		args = append(g.sudo[1:], args...)
		return g.run(bytes.NewReader(data), g.sudo[0], args...)
	}
	return g.run(bytes.NewReader(data), args[0], args[1:]...)
}
