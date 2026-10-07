package providers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The retired session-check hook is removed; the operator's own hooks,
// including ones sharing its group, stay.
func TestRemoveCodexSessionHook(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	in := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"` + legacySessionHook + `","timeout":5}]},` +
		`{"hooks":[{"type":"command","command":"mine"}]}],"Stop":[{"hooks":[{"type":"command","command":"stop"}]}]}}`
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := removeCodexSessionHook(home); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := os.ReadFile(path)
	if strings.Contains(string(out), "session-check") || !strings.Contains(string(out), `"mine"`) || !strings.Contains(string(out), `"stop"`) {
		t.Fatalf("hooks.json = %s", out)
	}
	if err := removeCodexSessionHook(t.TempDir()); err != nil {
		t.Fatalf("a home without hooks.json: %v", err)
	}
}
