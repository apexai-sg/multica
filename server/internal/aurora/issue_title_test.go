package aurora

import (
	"strings"
	"testing"
)

func TestIssueTitleUsesDisplayNameAndFortyRunes(t *testing.T) {
	got := IssueTitle("海报制作", "  "+strings.Repeat("中", 41))
	want := "海报制作：" + strings.Repeat("中", 40)
	if got != want {
		t.Fatalf("IssueTitle() = %q, want %q", got, want)
	}
}
