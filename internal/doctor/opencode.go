package doctor

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OpenCodeSuite is the OpenCode provider's doctor checks: its CLI, login
// status via `opencode auth list`, and its host-wide server status.
func OpenCodeSuite(name, binary string) Suite {
	var st opencodeState
	return Suite{
		Prepare: func(d *Check) error {
			var err error
			st, err = d.collectOpenCodeState()
			return err
		},
		Install: func(d *Check) {
			d.checkOpenCodeCLI(binary)
		},
		Head: func(d *Check) { d.checkOpenCodeHead(st) },
	}
}

// opencodeState is what doctor learns about the OpenCode environment once and reuses.
type opencodeState struct {
	// installed is true if the opencode binary is runnable
	installed bool
	// version is the CLI version
	version string
	// authenticated is true if `opencode auth list` succeeds
	authenticated bool
	// serverRunning is true if opencode-server.service is active
	serverRunning bool
	// serverHealthy is true if the server responds correctly to health checks
	serverHealthy bool
}

func (d *Check) collectOpenCodeState() (opencodeState, error) {
	st := opencodeState{}

	// Check if opencode binary is installed and runnable
	if _, err := d.opts.LookPath("opencode"); err == nil {
		st.installed = true
		// Get version
		stdout, _, _, _ := d.opts.Runner.Run("opencode", "--version")
		st.version = strings.TrimSpace(stdout)
	}

	// Check if authenticated via opencode auth list
	stdout, _, _, err := d.opts.Runner.Run("opencode", "auth", "list")
	if err == nil && strings.TrimSpace(stdout) != "" {
		st.authenticated = true
	}

	// Check if server unit is running
	_, _, _, err = d.opts.Runner.Run("systemctl", "--user", "is-active", "--quiet", "opencode-server.service")
	if err == nil {
		st.serverRunning = true

	st.serverHealthy = d.checkOpenCodeServerHealth()
	}

	return st, nil
}

func (d *Check) checkOpenCodeCLI(binary string) {
	// Try to find the binary in home-relative paths first
	home := d.home()
	candidatePaths := []string{filepath.Join(home, ".opencode", "bin", binary)}

	var found string
	for _, path := range candidatePaths {
		if _, err := os.Stat(path); err == nil && d.succeeds(path, "--version") {
			found = path
			break
		}
	}

	// Fall back to PATH lookup
	if found == "" {
		if _, err := d.opts.LookPath(binary); err == nil && d.succeeds(binary, "--version") {
			found = binary
		}
	}

	if found != "" {
		d.Pass("%s --version runs", binary)
	} else {
		d.Fail("%s is missing or not runnable", binary)
	}
}

// checkOpenCodeHead covers the OpenCode login and server status.
func (d *Check) checkOpenCodeHead(st opencodeState) {
	if st.installed {
		if st.version != "" {
			d.Pass("OpenCode CLI version: %s", st.version)
		}
	}

	if st.authenticated {
		d.Pass("OpenCode is authenticated (opencode auth list succeeds)")
	}

	if st.serverRunning {
		d.Pass("opencode-server.service is active")
		d.checkHeadShellPath(true, "opencode-server.service")

		if st.serverHealthy {
			d.Pass("OpenCode server health check succeeded")
		} else {
			d.Warn("OpenCode server health check failed or unreachable")
		}
	} else {
		d.Warn("opencode-server.service is not currently running (enable with `codvps enable opencode`)")
	}
}

// openCodeHealthURL is the managed server's health endpoint; tests point it
// at a local fake.
var openCodeHealthURL = "http://127.0.0.1:4096/global/health"

// checkOpenCodeServerHealth requires the managed server to refuse an
// unauthenticated request (401) and to report healthy with the password
// `codvps enable opencode` generated into <config>/opencode-env.
func (d *Check) checkOpenCodeServerHealth() bool {
	password, ok := openCodePassword(filepath.Join(d.opts.Layout.ConfigDir(), "opencode-env"))
	if !ok {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	get := func(auth bool) (*http.Response, error) {
		req, err := http.NewRequest("GET", openCodeHealthURL, nil)
		if err != nil {
			return nil, err
		}
		if auth {
			req.SetBasicAuth("opencode", password)
		}
		return client.Do(req)
	}

	resp, err := get(false)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return false
	}

	resp, err = get(true)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var health struct {
		Healthy bool `json:"healthy"`
	}
	body, _ := io.ReadAll(resp.Body)
	return json.Unmarshal(body, &health) == nil && health.Healthy
}

// openCodePassword reads OPENCODE_SERVER_PASSWORD from the server's
// environment file without ever printing it.
func openCodePassword(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, found := strings.CutPrefix(strings.TrimSpace(line), "OPENCODE_SERVER_PASSWORD="); found && v != "" {
			return v, true
		}
	}
	return "", false
}
