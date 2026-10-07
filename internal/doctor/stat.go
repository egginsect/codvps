package doctor

import (
	"os"
	"syscall"
)

// fileIDs returns a file's owning uid and gid.
func fileIDs(fi os.FileInfo) (int, int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
