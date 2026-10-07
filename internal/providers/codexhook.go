package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/egginsect/codvps/internal/systemd"
)

// legacySessionHook is the Codex SessionStart hook the removed runtime
// updater registered; `codvps internal session-check` is kept as a no-op
// so a leftover registration never fails a session.
const legacySessionHook = systemd.CodvpsBinary + " internal session-check"

// removeCodexSessionHook drops the legacy session-check hook from
// <codexHome>/hooks.json, leaving every other hook as it is.
func removeCodexSessionHook(codexHome string) error {
	path := filepath.Join(codexHome, "hooks.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not read %s: %w", path, err)
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%s must be a JSON object: %w", path, err)
	}
	hooks, _ := doc["hooks"].(map[string]any)
	start, _ := hooks["SessionStart"].([]any)
	var kept []any
	for _, group := range start {
		g, _ := group.(map[string]any)
		inner, _ := g["hooks"].([]any)
		var rest []any
		for _, h := range inner {
			if m, _ := h.(map[string]any); m["command"] != legacySessionHook {
				rest = append(rest, h)
			}
		}
		if len(rest) == len(inner) {
			kept = append(kept, group)
		} else if len(rest) > 0 {
			g["hooks"] = rest
			kept = append(kept, g)
		}
	}
	if len(kept) == len(start) && fmt.Sprint(kept) == fmt.Sprint(start) {
		return nil
	}
	if len(kept) == 0 {
		delete(hooks, "SessionStart")
	} else {
		hooks["SessionStart"] = kept
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".codvps-tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
