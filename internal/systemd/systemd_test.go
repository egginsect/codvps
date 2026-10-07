package systemd

import (
	"testing"

	"github.com/egginsect/codvps/internal/runner"
)

func TestUserManagerEnable(t *testing.T) {
	fake := runner.NewFakeRunner()
	fake.SetResponse("systemctl", []string{"--user", "enable", "test.service"}, runner.Response{
		ExitCode: 0,
	})

	mgr := NewUserManager(fake)
	err := mgr.Enable("test.service")

	if err != nil {
		t.Errorf("Enable() error = %v, want nil", err)
	}

	calls := fake.GetCalls()
	if len(calls) != 1 {
		t.Errorf("expected 1 call, got %d", len(calls))
	}
}

func TestSystemManagerEnable(t *testing.T) {
	fake := runner.NewFakeRunner()
	fake.SetResponse("systemctl", []string{"enable", "test.service"}, runner.Response{
		ExitCode: 0,
	})

	mgr := NewSystemManager(fake)
	err := mgr.Enable("test.service")

	if err != nil {
		t.Errorf("Enable() error = %v, want nil", err)
	}

	calls := fake.GetCalls()
	if len(calls) != 1 {
		t.Errorf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Args[0] != "enable" {
		t.Errorf("expected enable call, got %v", calls[0].Args)
	}
}

func TestEnableFailure(t *testing.T) {
	fake := runner.NewFakeRunner()
	fake.SetResponse("systemctl", []string{"--user", "enable", "missing.service"}, runner.Response{
		ExitCode: 1,
		Stderr:   "Unit file missing.service not found.",
	})

	mgr := NewUserManager(fake)
	err := mgr.Enable("missing.service")

	if err == nil {
		t.Errorf("Enable() error = nil, want non-nil")
	}
}

func TestStart(t *testing.T) {
	fake := runner.NewFakeRunner()
	fake.SetResponse("systemctl", []string{"--user", "start", "test.service"}, runner.Response{
		ExitCode: 0,
	})

	mgr := NewUserManager(fake)
	err := mgr.Start("test.service")

	if err != nil {
		t.Errorf("Start() error = %v, want nil", err)
	}
}

func TestStop(t *testing.T) {
	fake := runner.NewFakeRunner()
	fake.SetResponse("systemctl", []string{"--user", "stop", "test.service"}, runner.Response{
		ExitCode: 0,
	})

	mgr := NewUserManager(fake)
	err := mgr.Stop("test.service")

	if err != nil {
		t.Errorf("Stop() error = %v, want nil", err)
	}
}

func TestRestart(t *testing.T) {
	fake := runner.NewFakeRunner()
	fake.SetResponse("systemctl", []string{"--user", "restart", "test.service"}, runner.Response{
		ExitCode: 0,
	})

	mgr := NewUserManager(fake)
	err := mgr.Restart("test.service")

	if err != nil {
		t.Errorf("Restart() error = %v, want nil", err)
	}
}

func TestDaemonReload(t *testing.T) {
	fake := runner.NewFakeRunner()
	fake.SetResponse("systemctl", []string{"--user", "daemon-reload"}, runner.Response{
		ExitCode: 0,
	})

	mgr := NewUserManager(fake)
	err := mgr.DaemonReload()

	if err != nil {
		t.Errorf("DaemonReload() error = %v, want nil", err)
	}
}

func TestIsActive(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		want     bool
	}{
		{"active", 0, true},
		{"inactive", 3, false},
		{"other error", 1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := runner.NewFakeRunner()
			fake.SetResponse("systemctl", []string{"--user", "is-active", "--quiet", "test.service"}, runner.Response{
				ExitCode: tt.exitCode,
			})

			mgr := NewUserManager(fake)
			active := mgr.IsActive("test.service")

			if active != tt.want {
				t.Errorf("IsActive() = %v, want %v", active, tt.want)
			}
		})
	}
}

func TestStatus(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		wantErr  bool
	}{
		{"active", 0, false},
		{"inactive", 3, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := runner.NewFakeRunner()
			fake.SetResponse("systemctl", []string{"--user", "is-active", "--quiet", "test.service"}, runner.Response{
				ExitCode: tt.exitCode,
			})

			mgr := NewUserManager(fake)
			err := mgr.Status("test.service")

			if (err != nil) != tt.wantErr {
				t.Errorf("Status() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
