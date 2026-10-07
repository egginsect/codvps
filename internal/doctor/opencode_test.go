package doctor

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/configlink"
	"github.com/egginsect/codvps/internal/paths"
)

// fakeOpenCode answers /global/health like `opencode serve` with basic
// auth: 401 without the password, {"healthy":true} with it.
func fakeOpenCode(t *testing.T, password string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "opencode" || pass != password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"healthy":true,"version":"1.18.33"}`))
	}))
	t.Cleanup(srv.Close)
	old := openCodeHealthURL
	openCodeHealthURL = srv.URL + "/global/health"
	t.Cleanup(func() { openCodeHealthURL = old })
}

func openCodeCheck(t *testing.T, envFile string) *Check {
	t.Helper()
	layout, err := paths.NewForHome(t.TempDir(), "codvps")
	if err != nil {
		t.Fatal(err)
	}
	if envFile != "" {
		if err := os.MkdirAll(layout.ConfigDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(layout.ConfigDir(), "opencode-env"), []byte(envFile), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &Check{opts: Options{Layout: layout}}
}

// TestOpenCodeHealthUsesGeneratedPassword: the check authenticates with the
// password `codvps enable opencode` wrote, not a fixed one.
func TestOpenCodeHealthUsesGeneratedPassword(t *testing.T) {
	fakeOpenCode(t, "s3cret-generated")
	if !openCodeCheck(t, "OPENCODE_SERVER_PASSWORD=s3cret-generated\n").checkOpenCodeServerHealth() {
		t.Fatal("healthy server with the generated password reported unhealthy")
	}
}

// TestOpenCodeHealthFailsWithWrongOrMissingPassword keeps the check honest.
func TestOpenCodeHealthFailsWithWrongOrMissingPassword(t *testing.T) {
	fakeOpenCode(t, "right")
	if openCodeCheck(t, "OPENCODE_SERVER_PASSWORD=wrong\n").checkOpenCodeServerHealth() {
		t.Error("a wrong password passed the health check")
	}
	if openCodeCheck(t, "").checkOpenCodeServerHealth() {
		t.Error("a missing password file passed the health check")
	}
}

// TestConfigLinkDrift: doctor passes a fully linked config repo and warns
// when a tool replaced a link with a regular file.
func TestConfigLinkDrift(t *testing.T) {
	layout, err := paths.NewForHome(t.TempDir(), "codvps")
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(layout.Home(), "cfg")
	src := filepath.Join(checkout, "claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := configlink.Link(layout.Home(), layout.ConfigDir(), checkout); err != nil {
		t.Fatal(err)
	}
	run := func() string {
		var out strings.Builder
		d := &Check{opts: Options{Layout: layout}, out: &out}
		d.checkConfigLink()
		return out.String()
	}
	if got := run(); !strings.Contains(got, "PASS: config repo "+checkout+" is linked") {
		t.Fatalf("linked repo: %q", got)
	}
	target := filepath.Join(layout.Home(), ".claude", "settings.json")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(); !strings.Contains(got, "WARN: "+target+" is no longer a link") {
		t.Fatalf("replaced link: %q", got)
	}
}
