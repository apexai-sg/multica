package aurora

import "strings"

// IssueTitle renders the issue title for a generation: the skill's display name
// joined to the first 40 runes of the trimmed prompt with a full-width colon.
// The prompt is trimmed before truncation so leading whitespace cannot consume
// the visible budget, and truncation counts runes rather than bytes so a
// multi-byte prompt is never split mid-character.
func IssueTitle(displayName, prompt string) string {
	runes := []rune(strings.TrimSpace(prompt))
	if len(runes) > 40 {
		runes = runes[:40]
	}
	return strings.TrimSpace(displayName) + "：" + string(runes)
}
