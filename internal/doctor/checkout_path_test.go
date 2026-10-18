package doctor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/runner"
)

func TestDoctorClaudeUsesRegisteredExternalPath(t *testing.T) {
	h := healthyHost(t)
	dest := filepath.Join(t.TempDir(), "Workspace Shared", "hello")
	if err := os.MkdirAll(filepath.Join(dest, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := registry.WriteEntries(h.layout.RepositoriesPath(), []registry.Entry{{Name: "hello", Path: dest}}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(h.home, "hello")); err != nil {
		t.Fatal(err)
	}
	out, _, err := h.doctor()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "PASS: repository hello has an active Claude unit") || strings.Contains(out, "has no repository") {
		t.Fatal(out)
	}
}

func TestDoctorCursorUsesRegisteredExternalPath(t *testing.T) {
	h := healthyHost(t)
	dest := filepath.Join(t.TempDir(), "Workspace Shared", "cursor checkout")
	if err := os.MkdirAll(filepath.Join(dest, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := registry.WriteEntries(h.layout.RepositoriesPath(), []registry.Entry{{Name: "hello", Path: dest}}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(h.home, "hello")); err != nil {
		t.Fatal(err)
	}
	fr := runner.NewFakeRunner()
	fr.SetResponse("git", []string{"-C", dest, "rev-parse", "--path-format=absolute", "--git-common-dir"}, runner.Response{Stdout: filepath.Join(dest, ".git") + "\n"})
	fr.SetResponse("systemctl", []string{"--user", "is-active", "cursor-remote@hello.service"}, runner.Response{Stdout: "active\n"})
	var out bytes.Buffer
	d := &Check{opts: Options{Layout: h.layout, Runner: fr, Root: h.root}, out: &out}
	d.checkCursorHead(cursorState{authenticated: true, units: []string{"cursor-remote@hello.service"}})
	if !strings.Contains(out.String(), "PASS: repository hello has an active Cursor unit") || strings.Contains(out.String(), "has no repository") {
		t.Fatal(out.String())
	}
}
