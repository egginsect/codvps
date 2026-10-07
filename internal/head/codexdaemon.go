package head

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ControlSocket is the Codex app-server's control socket under a Codex
// home: the path Codex itself binds and its CLI connects to.
func ControlSocket(codexHome string) string {
	return filepath.Join(codexHome, "app-server-control", "app-server-control.sock")
}

// procNetUnix is the kernel's table of unix sockets.
const procNetUnix = "net/unix"

// soAcceptcon is the __SO_ACCEPTCON flag the kernel sets on a unix socket
// that is listening.
const soAcceptcon = 0x00010000

// ControlSocketListening is the production liveness probe: the daemon is up
// when a listening unix stream socket is bound to the control socket's
// resolved path. It never connects: the daemon speaks WebSocket on that
// socket and logs a warning for every client that closes without a
// handshake, which a probe every minute would repeat forever.
func ControlSocketListening(socket string) error {
	return controlSocketListening("/proc", socket)
}

// controlSocketListening probes against procRoot, /proc in production.
func controlSocketListening(procRoot, socket string) error {
	target, err := filepath.EvalSymlinks(socket)
	if err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(procRoot, procNetUnix))
	if err != nil {
		return fmt.Errorf("failed to list unix sockets: %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// Num RefCount Protocol Flags Type St Inode Path
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 || fields[7] != target {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil {
			continue
		}
		if flags&soAcceptcon != 0 && fields[4] == "0001" {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("failed to read unix sockets: %w", err)
	}
	return fmt.Errorf("no process is listening on %s", target)
}

// probeDaemon decides whether the Codex Remote daemon is live by
// checking that its control socket is listening (without connecting). It
// returns false plus the reason when it is not.
func (c *Codex) probeDaemon() (bool, string) {
	socket := c.controlSocket()
	if err := c.listening(socket); err != nil {
		return false, fmt.Sprintf("cannot connect to the app-server control socket %s: %v", socket, err)
	}
	return true, ""
}

// RemoveDanglingControlSocket removes the control socket path under
// codexHome only when it is a symlink whose target does not exist. The
// retired sandboxed unit left such a link pointing into its private /tmp,
// which the host's codex CLI cannot follow. Anything else at that path (a
// live socket, a link that resolves, a regular file) is left alone. It
// reports whether a link was removed.
func RemoveDanglingControlSocket(codexHome string) (bool, error) {
	socket := ControlSocket(codexHome)
	fi, err := os.Lstat(socket)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("failed to inspect %s: %w", socket, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return false, nil
	}
	if _, err := os.Stat(socket); err == nil || !errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err := os.Remove(socket); err != nil {
		return false, fmt.Errorf("failed to remove the dangling control socket link %s: %w", socket, err)
	}
	return true, nil
}

// DanglingControlSocket reports whether the control socket path under
// codexHome is a symlink whose target does not exist.
func DanglingControlSocket(codexHome string) bool {
	socket := ControlSocket(codexHome)
	fi, err := os.Lstat(socket)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	_, err = os.Stat(socket)
	return errors.Is(err, os.ErrNotExist)
}
