package handler

import (
	"encoding/json"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestApplyAuroraIssueClaimCopiesPromptAndIssue pins the claim-side half of an
// issue-bound Aurora round: the task context carries the trusted type plus the
// prompt, attachments and generation id, and the handler copies them onto the
// response while leaving the issue binding untouched. A context of any other
// type (or none) must be left to the existing quick-create branch.
func TestApplyAuroraIssueClaimCopiesPromptAndIssue(t *testing.T) {
	t.Parallel()

	ctx, err := json.Marshal(map[string]any{
		"type":           "aurora_issue",
		"generation_id":  "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"prompt":         "make the poster blue",
		"attachment_ids": []string{"11111111-1111-4111-8111-111111111111"},
	})
	if err != nil {
		t.Fatalf("marshal context: %v", err)
	}

	resp := AgentTaskResponse{IssueID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
	if !applyAuroraIssueClaim(&resp, db.AgentTaskQueue{Context: ctx}) {
		t.Fatal("applyAuroraIssueClaim returned false for an aurora_issue context")
	}
	if resp.QuickCreatePrompt != "make the poster blue" {
		t.Errorf("QuickCreatePrompt = %q, want the context prompt", resp.QuickCreatePrompt)
	}
	if resp.GenerationID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" {
		t.Errorf("GenerationID = %q, want the context generation id", resp.GenerationID)
	}
	if got := resp.QuickCreateAttachmentIDs; len(got) != 1 || got[0] != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("QuickCreateAttachmentIDs = %v, want the context attachment id", got)
	}
	if resp.IssueID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Errorf("IssueID = %q, want the issue binding preserved", resp.IssueID)
	}

	// A quick-create context is not an issue round, and a missing context must
	// not be claimed either — both keep the existing behaviour.
	if applyAuroraIssueClaim(&AgentTaskResponse{}, db.AgentTaskQueue{Context: []byte(`{"type":"quick_create","prompt":"x"}`)}) {
		t.Error("applyAuroraIssueClaim accepted a quick_create context")
	}
	if applyAuroraIssueClaim(&AgentTaskResponse{}, db.AgentTaskQueue{}) {
		t.Error("applyAuroraIssueClaim accepted an empty context")
	}
}
