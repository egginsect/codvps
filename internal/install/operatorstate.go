package install

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egginsect/codvps/internal/fsutil"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/runner"
)

// EnsureOperatorState is `codvps internal operator-state [<provider>...]`:
// it runs as the operator (never as root), so a symlink the operator
// controls can never redirect a privileged chmod or write. It creates the
// private codvps state root, keeps ~/.local/bin on interactive shells'
// PATH, and runs each named provider's own operator-state step (for Codex:
// the private Codex Remote home and the legacy workspace migration).
func EnsureOperatorState(layout *paths.Layout, git runner.Runner, selected providers.Set) error {
	if os.Geteuid() == 0 {
		return errors.New("codvps internal operator-state must run as the operator, not as root")
	}
	if err := fsutil.EnsurePrivateDir(layout.ConfigDir()); err != nil {
		return fmt.Errorf("codvps state root creation is unsafe: %w", err)
	}
	// The managed-runtime selection lives here.
	if err := fsutil.EnsurePrivateDir(filepath.Join(layout.ConfigDir(), "runtimes")); err != nil {
		return fmt.Errorf("codvps runtime state creation is unsafe: %w", err)
	}
	if err := ensurePathLine(filepath.Join(layout.Home(), ".bashrc")); err != nil {
		return err
	}
	for _, p := range selected {
		if p.Install == nil || p.Install.OperatorState == nil {
			continue
		}
		if err := p.Install.OperatorState(layout, git); err != nil {
			return err
		}
	}
	return nil
}

// ensurePathLine appends pathLine to ~/.bashrc unless it is already there.
func ensurePathLine(bashrc string) error {
	data, err := os.ReadFile(bashrc)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to read %s: %w", bashrc, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == pathLine {
			return nil
		}
	}
	f, err := os.OpenFile(bashrc, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", bashrc, err)
	}
	prefix := ""
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		prefix = "\n"
	}
	if _, err := f.WriteString(prefix + pathLine + "\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to update %s: %w", bashrc, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to update %s: %w", bashrc, err)
	}
	return nil
}
