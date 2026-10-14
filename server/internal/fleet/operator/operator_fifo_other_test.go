//go:build !windows

package operator

import (
	"os"
	"syscall"
)

// FIFOs exist only on the platforms the Fleet runs on. These cases prove that a
// substituted pipe cannot block or redirect a private-file open; the Windows
// stub beside this file keeps the package buildable without them.
var (
	configReplacementCases = []string{"symlink", "fifo"}
	homeRootCases          = []string{"absent", "malformed", "fifo"}
)

func makeFifo(path string, mode uint32) error { return syscall.Mkfifo(path, mode) }

// releaseBlockedFifo unblocks a FIFO open that validation refused to complete,
// so a failing test leaves no blocked goroutine behind.
func releaseBlockedFifo(path string) {
	f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return
	}
	_, _ = f.Write([]byte("{}"))
	_ = f.Close()
}
