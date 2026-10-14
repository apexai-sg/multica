//go:build !windows

package util

import (
	"os"
	"syscall"
)

// OpenPrivateFile opens an explicitly supplied private file for reading.
// O_NOFOLLOW enforces the final-component boundary at open rather than in a
// racy Lstat, and O_NONBLOCK keeps a substituted FIFO from hanging before the
// caller validates the descriptor. Callers still check that the result is a
// regular file with private permissions.
func OpenPrivateFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// OwnedByCurrentUser reports whether info is owned by the process's effective
// user and group. A private-file boundary uses it to reject a file another
// account could rewrite.
func OwnedByCurrentUser(info os.FileInfo) bool {
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(s.Uid) == os.Geteuid() && int(s.Gid) == os.Getegid()
}
