package aurora

import "testing"

// TestToolExpertsAndToolIDsShareOneKeySet holds the two tables to the same
// contract from the inside: toolIDs is the shipped page order and toolExperts
// is the executor resolution, and an id present in only one of them is either a
// page that cannot run or a mapping no page reaches. The external test package
// can only observe the intersection through the accessors, so the difference in
// either direction is asserted here.
func TestToolExpertsAndToolIDsShareOneKeySet(t *testing.T) {
	if len(toolExperts) != len(toolIDs) {
		t.Fatalf("toolExperts has %d keys, toolIDs has %d", len(toolExperts), len(toolIDs))
	}
	listed := make(map[string]bool, len(toolIDs))
	for _, id := range toolIDs {
		listed[id] = true
	}
	for id := range toolExperts {
		if !listed[id] {
			t.Errorf("%q is mapped but not listed in toolIDs", id)
		}
	}
}
