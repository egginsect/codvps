package providers

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/egginsect/codvps/internal/runner"
)

// Installed is the provider's installed CLI, the first executable of its
// Provision.Present paths (root is the system root, "/" in production), or
// "" when it has none.
func (p *Provider) Installed(root, home string) string {
	if p.Provision == nil {
		return ""
	}
	for _, path := range p.Provision.Present {
		resolved := filepath.Join(root, path)
		if rest, ok := strings.CutPrefix(path, "~/"); ok {
			resolved = filepath.Join(home, rest)
		}
		if isExecutable(resolved) {
			return resolved
		}
	}
	return ""
}

// OperatorProvisioner runs vendor installers and updaters as the operator
// who invoked codvps, the way `codvps enable`, `login` and `update` run.
// Root-only steps are refused with the command that performs them.
type OperatorProvisioner struct {
	HomeDir string
	Runner  runner.Runner
	Out     io.Writer
	Diag    io.Writer
}

// Home implements Provisioner.
func (o *OperatorProvisioner) Home() string { return o.HomeDir }

// Cmd implements Provisioner: a root step cannot run here.
func (o *OperatorProvisioner) Cmd(name string, args ...string) error {
	return fmt.Errorf("%s needs root; run sudo codvps install", strings.Join(append([]string{name}, args...), " "))
}

// AsOperator implements Provisioner: the snippet runs under bash with a
// scrubbed environment, the same one install gives vendor installers.
func (o *OperatorProvisioner) AsOperator(env []string, script string) error {
	args := append([]string{"-i", "HOME=" + o.HomeDir, "PATH=" + o.HomeDir + "/.local/bin:/usr/local/bin:/usr/bin:/bin"}, env...)
	args = append(args, "bash", "-o", "pipefail", "-c", script)
	_, _ = fmt.Fprintf(o.Out, "+ %s\n", script)
	code, err := o.Runner.RunWithIO("env", args, nil, o.Out, o.Diag)
	if code != 0 || err != nil {
		return fmt.Errorf("%s failed (exit %d): %v", script, code, err)
	}
	return nil
}

// EnsureInstalled installs p's CLI with its vendor installer when it is
// missing, so a CLI is only ever downloaded once it is used. It reports
// whether it installed anything.
func EnsureInstalled(p *Provider, prov Provisioner, root string) (bool, error) {
	if p.Provision == nil || p.Provision.Run == nil || p.Installed(root, prov.Home()) != "" {
		return false, nil
	}
	if !p.Provision.AsOperator && os.Geteuid() != 0 {
		return false, fmt.Errorf("%s is not installed and its installer needs root; run sudo codvps install --components %s", p.Name, p.Name)
	}
	if err := p.Provision.Run(prov); err != nil {
		return false, fmt.Errorf("could not install %s: %w", p.Label, err)
	}
	if p.Installed(root, prov.Home()) == "" {
		return false, fmt.Errorf("the %s installer finished but no %s executable is present", p.Label, p.Binary)
	}
	return true, nil
}

// Version is the first line bin prints for --version, or "unknown".
func Version(r runner.Runner, bin string) string {
	out, _, code, err := r.Run(bin, "--version")
	if code != 0 || err != nil {
		return "unknown"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return "unknown"
}

// UpdateResult is one provider's `codvps update` outcome.
type UpdateResult struct {
	Provider *Provider
	Before   string
	After    string
}

// Changed reports whether the update moved the CLI to another version.
func (u UpdateResult) Changed() bool { return u.Before != u.After }

// Update runs p's vendor updater on its installed CLI and reports the
// version before and after.
func Update(p *Provider, prov Provisioner, r runner.Runner, root string) (UpdateResult, error) {
	res := UpdateResult{Provider: p}
	bin := p.Installed(root, prov.Home())
	if bin == "" {
		return res, fmt.Errorf("%s is not installed; codvps enable %s installs it", p.Name, p.Name)
	}
	if p.Provision.Update == nil {
		return res, fmt.Errorf("%s has no vendor updater", p.Name)
	}
	res.Before = Version(r, bin)
	if err := p.Provision.Update(prov, bin); err != nil {
		return res, fmt.Errorf("could not update %s: %w", p.Label, err)
	}
	if bin = p.Installed(root, prov.Home()); bin == "" {
		return res, fmt.Errorf("%s is missing after its update", p.Label)
	}
	res.After = Version(r, bin)
	return res, nil
}

// Unit is one head unit an update may restart.
type Unit struct {
	Name string
	User bool
}

// ActiveUnits are p's head units that are active now: a disabled or
// deliberately stopped head is never started by an update.
func ActiveUnits(p *Provider, r runner.Runner, operator string) []Unit {
	if p.Provision == nil {
		return nil
	}
	rs := p.Provision.Restart
	systemctl := func(args ...string) string {
		if rs.User {
			args = append([]string{"--user"}, args...)
		}
		out, _, _, _ := r.Run("systemctl", args...)
		return out
	}
	var names []string
	switch {
	case rs.Glob != "":
		for _, line := range strings.Split(systemctl("list-units", rs.Glob, "--plain", "--no-legend", "--no-pager"), "\n") {
			if f := strings.Fields(line); len(f) > 0 {
				if ok, _ := path.Match(rs.Glob, f[0]); ok {
					names = append(names, f[0])
				}
			}
		}
	case rs.Unit != nil:
		names = append(names, rs.Unit(operator))
	}
	var out []Unit
	for _, name := range names {
		if strings.TrimSpace(systemctl("is-active", name)) == "active" {
			out = append(out, Unit{Name: name, User: rs.User})
		}
	}
	return out
}

// RestartUnit restarts a head after clearing its failed state; a system
// unit goes through sudo.
func RestartUnit(r runner.Runner, u Unit) error {
	run := func(args ...string) error {
		name := "systemctl"
		if u.User {
			args = append([]string{"--user"}, args...)
		} else {
			name, args = "sudo", append([]string{"systemctl"}, args...)
		}
		_, stderr, code, err := r.Run(name, args...)
		if code != 0 || err != nil {
			if msg := strings.TrimSpace(stderr); msg != "" {
				return errors.New(msg)
			}
			return fmt.Errorf("%s %s: exit %d: %v", name, strings.Join(args, " "), code, err)
		}
		return nil
	}
	_ = run("reset-failed", u.Name) // nothing to reset is not an error
	return run("restart", u.Name)
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
