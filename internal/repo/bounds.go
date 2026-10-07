package repo

import (
	"fmt"
	"os"

	"github.com/egginsect/codvps/internal/registry"
)

// maxStateFileBytes bounds every read of the registry file: it is
// operator-writable, so an unbounded read would let a corrupted or hostile
// file exhaust memory. 1 MiB comfortably fits many thousands of repository
// names; any real registry is orders of magnitude smaller.
const maxStateFileBytes = 1 << 20

// checkFileSizeBound rejects a file larger than max before it is read via
// registry.Read (which internal/repo does not own and so cannot bound
// internally). A missing file is fine.
func checkFileSizeBound(path string, max int64) error {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Size() > max {
		return fmt.Errorf("%s exceeds the maximum allowed size of %d bytes", path, max)
	}
	return nil
}

// readNamesBounded is registry.Read with a size check first, used at every
// call site in this package that reads a registry-format (one name per
// line) file that an operator -- or a corrupted/hostile write -- controls.
func readNamesBounded(path string) ([]string, error) {
	if err := checkFileSizeBound(path, maxStateFileBytes); err != nil {
		return nil, err
	}
	return registry.Read(path)
}
