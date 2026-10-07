package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// addProc writes one fake process: its environment, parent, executable and
// arguments.
func addProc(t *testing.T, h *host, pid, ppid int, exe string, environ string, args ...string) {
	dir := filepath.Join(h.proc, fmt.Sprint(pid))
	write(t, filepath.Join(dir, "environ"), environ, 0o444)
	write(t, filepath.Join(dir, "stat"), fmt.Sprintf("%d (%s) S %d 1 1 0\n", pid, filepath.Base(exe), ppid), 0o444)
	write(t, filepath.Join(dir, "cmdline"), strings.Join(args, "\x00")+"\x00", 0o444)
	if err := os.Symlink(exe, filepath.Join(dir, "exe")); err != nil {
		t.Fatal(err)
	}
}

// addCgroup lists pids as the processes of a unit's control group.
func addCgroup(t *testing.T, h *host, unit, cg string, pids ...int) {
	var b strings.Builder
	for _, p := range pids {
		fmt.Fprintln(&b, p)
	}
	write(t, filepath.Join(h.cgroup, cg, "cgroup.procs"), b.String(), 0o644)
	if h.run.controlGroup == nil {
		h.run.controlGroup = map[string]string{}
	}
	h.run.controlGroup[unit] = cg
}

// A healthy host whose Claude unit runs as pid 777 and whose oneshot Codex
// unit has no main process: its daemon, pid 4242, sits in the unit's
// cgroup beside a child, each with an environment in the fake /proc.
func shellPathHost(t *testing.T, claudePath, codexPath string) *host {
	h := healthyHost(t)
	h.run.mainPID = map[string]string{"claude-remote@hello.service": "777", "codex-remote@operator.service": "0"}
	write(t, filepath.Join(h.proc, "777", "environ"), "HOME=/h\x00PATH="+claudePath+"\x00", 0o444)
	addProc(t, h, 4242, 1, "/home/user/.codex/packages/standalone/releases/x/codex", "PATH="+codexPath+"\x00", "codex", "app-server", "--listen", "unix://")
	addProc(t, h, 4243, 4242, "/usr/bin/bwrap", "PATH=/child\x00", "bwrap")
	addCgroup(t, h, "codex-remote@operator.service", "/system.slice/system-codex\\x2dremote.slice/codex-remote@operator.service", 4242, 4243)
	return h
}

func TestDoctorPassesHeadsRunningWithTheLoginShellPath(t *testing.T) {
	h := shellPathHost(t, "/login/bin:/usr/bin", "/login/bin:/usr/bin")
	h.shellPath = func([]string) (string, error) { return "/login/bin:/usr/bin", nil }
	out, rep, err := h.doctor()
	if err != nil || rep.Failures != 0 {
		t.Fatalf("err=%v failures=%d:\n%s", err, rep.Failures, out)
	}
	mustContain(t, out,
		"PASS: claude-remote@hello.service runs with the login-shell PATH\n",
		"PASS: codex-remote@operator.service runs with the login-shell PATH\n")
	mustNotContain(t, out, "not running with the login-shell PATH")
}

func TestDoctorWarnsAboutAHeadWithAnotherPath(t *testing.T) {
	h := shellPathHost(t, "/usr/local/bin:/usr/bin:/bin", "/login/bin:/usr/bin")
	h.shellPath = func([]string) (string, error) { return "/login/bin:/usr/bin", nil }
	out, rep, _ := h.doctor()
	if rep.Failures != 0 {
		t.Fatalf("a PATH mismatch must only warn:\n%s", out)
	}
	mustContain(t, out,
		"WARN: claude-remote@hello.service is not running with the login-shell PATH; restart it\n",
		"PASS: codex-remote@operator.service runs with the login-shell PATH\n")
}

func TestDoctorWarnsOnceWhenTheLoginShellPathCannotBeCaptured(t *testing.T) {
	h := shellPathHost(t, "/a", "/a")
	h.shellPath = func([]string) (string, error) { return "", errors.New("timed out") }
	out, rep, _ := h.doctor()
	if rep.Failures != 0 {
		t.Fatalf("failures:\n%s", out)
	}
	if n := strings.Count(out, "cannot capture the login-shell PATH"); n != 1 {
		t.Fatalf("capture failure reported %d times:\n%s", n, out)
	}
	mustContain(t, out, "WARN: cannot capture the login-shell PATH to compare with the heads (timed out)\n")
	mustNotContain(t, out, "runs with the login-shell PATH")
}

func TestDoctorWarnsWhenAHeadHasNoReadableEnvironment(t *testing.T) {
	h := shellPathHost(t, "/a", "/a")
	h.run.mainPID = map[string]string{"claude-remote@hello.service": "0"}
	h.shellPath = func([]string) (string, error) { return "/a", nil }
	out, _, _ := h.doctor()
	mustContain(t, out, "WARN: claude-remote@hello.service is not running with the login-shell PATH; restart it (no main process found)\n")
}

func TestDoctorKeepsTheExistingClaudeEnvironmentChecks(t *testing.T) {
	h := shellPathHost(t, "/a", "/a")
	h.shellPath = func([]string) (string, error) { return "/a", nil }
	out, _, _ := h.doctor()
	mustContain(t, out,
		"PASS: installed unit clears Anthropic environment variables\n",
		"PASS: ANTHROPIC_API_KEY is unset\n")
}

func TestDoctorWarnsWhenTheCodexDaemonPidIsNotFound(t *testing.T) {
	h := shellPathHost(t, "/a", "/a")
	h.run.controlGroup = nil
	h.shellPath = func([]string) (string, error) { return "/a", nil }
	out, rep, _ := h.doctor()
	if rep.Failures != 0 {
		t.Fatalf("failures:\n%s", out)
	}
	mustContain(t, out, "WARN: codex-remote@operator.service is not running with the login-shell PATH; restart it (no main process found)\n")
}

func TestDoctorChecksEveryTopLevelProcessOfAUnitWithoutAMainPid(t *testing.T) {
	h := shellPathHost(t, "/a", "/a")
	addProc(t, h, 4250, 1, "/x/codex", "PATH=/stale\x00", "codex", "app-server", "daemon", "pid-update-loop")
	addCgroup(t, h, "codex-remote@operator.service", "/system.slice/codex-remote@operator.service", 4242, 4243, 4250)
	h.shellPath = func([]string) (string, error) { return "/a", nil }
	out, rep, _ := h.doctor()
	if rep.Failures != 0 {
		t.Fatalf("failures:\n%s", out)
	}
	mustContain(t, out, "WARN: codex-remote@operator.service is not running with the login-shell PATH; restart it (process 4250)\n")
	mustNotContain(t, out, "PASS: codex-remote@operator.service")
}

func TestDoctorPassesAUnitWhoseTwoTopLevelProcessesHaveTheLoginShellPath(t *testing.T) {
	h := shellPathHost(t, "/a", "/a")
	addProc(t, h, 4250, 1, "/x/codex", "PATH=/a\x00", "codex", "app-server", "daemon", "pid-update-loop")
	addCgroup(t, h, "codex-remote@operator.service", "/system.slice/codex-remote@operator.service", 4242, 4243, 4250)
	h.shellPath = func([]string) (string, error) { return "/a", nil }
	out, _, _ := h.doctor()
	mustContain(t, out, "PASS: codex-remote@operator.service runs with the login-shell PATH\n")
}

func TestDoctorWarnsWhenAUnitHasNoLaunchPATH(t *testing.T) {
	h := shellPathHost(t, "/a", "/a")
	h.shellPath = func([]string) (string, error) { return "/a", nil }
	d := &Check{opts: Options{Layout: h.layout, Operator: testOperator, ShellPath: h.shellPath}, out: new(strings.Builder)}
	if _, ok := d.loginShellPath("sshd.service"); ok {
		t.Fatal("an unknown unit must not get a guessed PATH")
	}
	if got := d.out.(*strings.Builder).String(); !strings.Contains(got, "WARN: cannot tell the launch PATH of sshd.service") {
		t.Fatalf("output: %q", got)
	}
}

func TestDoctorFallsBackToTheCgroupForAnyHeadWithoutAMainPid(t *testing.T) {
	h := shellPathHost(t, "/a", "/a")
	h.run.mainPID["claude-remote@hello.service"] = "0"
	addProc(t, h, 900, 1, "/usr/bin/node", "PATH=/a\x00", "node")
	addProc(t, h, 901, 900, "/usr/bin/sh", "PATH=/other\x00", "sh")
	addCgroup(t, h, "claude-remote@hello.service", "/user.slice/claude-remote@hello.service", 900, 901)
	h.shellPath = func([]string) (string, error) { return "/a", nil }
	out, _, _ := h.doctor()
	mustContain(t, out, "PASS: claude-remote@hello.service runs with the login-shell PATH\n")
}

func TestDoctorCapturesTheShellPathFromEachHeadsUnitEnvironment(t *testing.T) {
	h := shellPathHost(t, "/login/bin:/usr/local/bin:/usr/bin:/bin", "/login/bin:/usr/local/bin:/usr/bin:/bin")
	var bases [][]string
	h.shellPath = func(base []string) (string, error) {
		bases = append(bases, base)
		return "/login/bin:" + pathIn(base), nil
	}
	out, rep, err := h.doctor()
	if err != nil || rep.Failures != 0 {
		t.Fatalf("err=%v failures=%d:\n%s", err, rep.Failures, out)
	}
	mustContain(t, out,
		"PASS: claude-remote@hello.service runs with the login-shell PATH\n",
		"PASS: codex-remote@operator.service runs with the login-shell PATH\n")
	if len(bases) != 1 {
		t.Fatalf("claude and codex share a unit PATH, so one capture; got %d: %v", len(bases), bases)
	}
	want := []string{"HOME=" + h.home, "USER=" + testOperator, "LOGNAME=" + testOperator, "PATH=/usr/local/bin:/usr/bin:/bin"}
	if strings.Join(bases[0], "\n") != strings.Join(want, "\n") {
		t.Fatalf("base environment = %v, want %v", bases[0], want)
	}
}

func pathIn(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			return v
		}
	}
	return ""
}

func TestDoctorAcceptsAPathTheCLIPrependedItsOwnDirectoriesTo(t *testing.T) {
	h := shellPathHost(t, "/cli/tmp:/cli/bin:/login/bin:/usr/bin", "/cli/tmp:/cli/bin:/login/bin:/usr/bin")
	h.shellPath = func([]string) (string, error) { return "/login/bin:/usr/bin", nil }
	out, _, _ := h.doctor()
	mustContain(t, out,
		"PASS: claude-remote@hello.service runs with the login-shell PATH\n",
		"PASS: codex-remote@operator.service runs with the login-shell PATH\n")
}

func TestDoctorWarnsWhenTheExpectedPathIsOnlyInTheMiddle(t *testing.T) {
	h := shellPathHost(t, "/cli:/login/bin:/usr/bin:/extra", "/login/bin:/usr/bin:/extra")
	h.shellPath = func([]string) (string, error) { return "/login/bin:/usr/bin", nil }
	out, _, _ := h.doctor()
	mustContain(t, out,
		"WARN: claude-remote@hello.service is not running with the login-shell PATH; restart it\n",
		"WARN: codex-remote@operator.service is not running with the login-shell PATH; restart it\n")
}
