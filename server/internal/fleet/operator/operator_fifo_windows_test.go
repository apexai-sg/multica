//go:build windows

package operator

import "errors"

// Windows has no FIFOs, so the substituted-pipe cases are absent rather than
// failing; the rest of the private-file coverage still runs.
var (
	configReplacementCases = []string{"symlink"}
	homeRootCases          = []string{"absent", "malformed"}
)

func makeFifo(string, uint32) error { return errors.New("fifos are unsupported on windows") }

func releaseBlockedFifo(string) {}
