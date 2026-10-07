package repo

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
)

// IdentityLookup resolves operator identity without requiring the calling
// test to run as root or to chown real files: production compares real
// filesystem ownership (via os.Lstat) against the uid/gid this interface
// resolves for the operator account; tests inject a fake that reports the
// test process's own uid/gid as the "operator" identity.
type IdentityLookup interface {
	// OperatorHome resolves the operator's home directory (e.g. via getent
	// passwd). Returns an error if the operator is unknown.
	OperatorHome(operator string) (string, error)
	// OperatorIDs resolves the operator's uid/gid.
	OperatorIDs(operator string) (uid, gid int, err error)
	// EUID returns the running process's effective uid.
	EUID() int
}

// osIdentityLookup is the production IdentityLookup.
type osIdentityLookup struct{}

// NewOSIdentityLookup returns the production IdentityLookup, backed by
// os/user and the real process uid.
func NewOSIdentityLookup() IdentityLookup {
	return osIdentityLookup{}
}

func (osIdentityLookup) OperatorHome(operator string) (string, error) {
	u, err := user.Lookup(operator)
	if err != nil {
		return "", fmt.Errorf("could not resolve operator home: %w", err)
	}
	return u.HomeDir, nil
}

func (osIdentityLookup) OperatorIDs(operator string) (int, int, error) {
	u, err := user.Lookup(operator)
	if err != nil {
		return 0, 0, fmt.Errorf("could not resolve operator identity: %w", err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, fmt.Errorf("could not parse operator uid: %w", err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, fmt.Errorf("could not parse operator gid: %w", err)
	}
	return uid, gid, nil
}

func (osIdentityLookup) EUID() int {
	return os.Geteuid()
}
