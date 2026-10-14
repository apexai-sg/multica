//go:build windows

package util

import (
	"os"

	"golang.org/x/sys/windows"
)

// OpenPrivateFile opens an explicitly supplied private file for reading.
// Windows has no O_NOFOLLOW, so the final-component boundary uses the
// equivalent flag: FILE_FLAG_OPEN_REPARSE_POINT stops a symlink or junction in
// the last path component from redirecting the read. There is no O_NONBLOCK
// equivalent, and named pipes are not reached by a path like this one; callers
// still validate the descriptor after the open.
func OpenPrivateFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

// OwnedByCurrentUser reports whether info is owned by the current process.
// Windows has no uid/gid equivalent and the Fleet's private-file boundary is a
// POSIX contract, so this fails closed: an owner that cannot be verified is not
// trusted.
func OwnedByCurrentUser(os.FileInfo) bool { return false }
