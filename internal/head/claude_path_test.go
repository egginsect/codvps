package head

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egginsect/codvps/internal/claudeconfig"
	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/runner"
)

func TestClaude_TrustAndReconcileUseRegisteredCheckoutPath(t *testing.T) {
	h := newTestHost(t)
	h.login()
	dest := filepath.Join(t.TempDir(), "Workspace Shared", "checkout with spaces")
	if err := os.MkdirAll(filepath.Join(dest, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := registry.WriteEntries(h.layout.RepositoriesPath(), []registry.Entry{{Name: "custom", Path: dest}}); err != nil {
		t.Fatal(err)
	}
	h.git.SetResponse("git", []string{"-C", dest, "rev-parse", "--path-format=absolute", "--git-common-dir"}, runner.Response{Stdout: filepath.Join(dest, ".git") + "\n"})
	if err := h.claude.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	trusted, err := claudeconfig.WorkspaceTrusted(h.layout.Home(), dest)
	if err != nil || !trusted {
		t.Fatalf("registered checkout trust=%v, %v", trusted, err)
	}
	oldTrusted, err := claudeconfig.WorkspaceTrusted(h.layout.Home(), filepath.Join(h.layout.Home(), "custom"))
	if err != nil || oldTrusted {
		t.Fatalf("unregistered HOME path trust=%v, %v", oldTrusted, err)
	}
	if !h.unitUp("custom") || len(h.sd.refusals) != 0 {
		t.Fatalf("head did not launch trusted path; refusals=%v", h.sd.refusals)
	}
}
