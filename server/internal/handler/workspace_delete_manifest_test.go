package handler

import (
	"context"
	"sort"
	"testing"
)

type workspaceDeleteAction string

const (
	workspaceDelete       workspaceDeleteAction = "delete"
	workspaceDeleteDetach workspaceDeleteAction = "detach"
	workspaceDeleteKeep   workspaceDeleteAction = "keep"
	workspaceDeleteSettle workspaceDeleteAction = "settle"
)

// workspaceDeletionManifest is the schema coverage contract for workspace
// teardown. Adding a table requires an explicit ownership decision here; the
// handler deletion graph must then implement that decision before CI passes.
//
// Enforcement is split. This test checks the map against the live schema,
// TestDeleteWorkspace_DetachesRatherThanDeletes drives the real teardown to
// prove the workspaceDeleteDetach entries are implemented, and
// TestDeleteWorkspace_HandsSandboxNodesToFleet drives the teardown to prove the
// fleet_nodes workspaceDeleteSettle entry is a real handoff rather than an
// exemption: teardown enqueues the Fleet destroy intent before the node row is
// deleted. Plain workspaceDelete entries still have no equivalent graph check
// and depend on review.
var workspaceDeletionManifest = map[string]workspaceDeleteAction{
	"activity_log":                       workspaceDelete,
	"agent":                              workspaceDelete,
	"agent_builder_draft":                workspaceDelete,
	"agent_invocation_target":            workspaceDelete,
	"agent_runtime":                      workspaceDelete,
	"agent_skill":                        workspaceDelete,
	"agent_task_queue":                   workspaceDelete,
	"agent_to_label":                     workspaceDelete,
	"attachment":                         workspaceDelete,
	"aurora_artifact_staging":            workspaceDelete,
	"aurora_asset":                       workspaceDelete,
	"aurora_generation":                  workspaceDelete,
	"aurora_moderation_log":              workspaceDelete,
	"aurora_provider_run":                workspaceDelete,
	"aurora_sandbox_node":                workspaceDelete,
	"aurora_signup_bonus":                workspaceDeleteKeep, // Account-level eligibility, not workspace-owned.
	"aurora_subscription":                workspaceDeleteKeep, // The user's personal plan, not the workspace's.
	"autopilot":                          workspaceDelete,
	"autopilot_collaborator":             workspaceDelete,
	"autopilot_quota_period":             workspaceDelete,
	"autopilot_quota_reservation":        workspaceDelete,
	"autopilot_rule_version":             workspaceDelete,
	"autopilot_run":                      workspaceDelete,
	"autopilot_subscriber":               workspaceDelete,
	"autopilot_trigger":                  workspaceDelete,
	"channel_binding_token":              workspaceDelete,
	"channel_chat_context_generation":    workspaceDelete,
	"channel_chat_session_binding":       workspaceDelete,
	"channel_inbound_audit":              workspaceDelete,
	"channel_inbound_message_dedup":      workspaceDelete,
	"channel_installation":               workspaceDelete,
	"channel_media_pending_object":       workspaceDeleteSettle,
	"channel_outbound_card_message":      workspaceDelete,
	"channel_outbound_message":           workspaceDelete,
	"channel_reply_delivery":             workspaceDelete,
	"channel_task_delivery":              workspaceDelete,
	"channel_typing_reaction":            workspaceDeleteSettle, // Source deletion makes retained anchors eligible for the cleanup worker.
	"channel_user_binding":               workspaceDelete,
	"chat_draft_restore":                 workspaceDelete,
	"chat_message":                       workspaceDelete,
	"chat_pinned_agent":                  workspaceDelete,
	"chat_session":                       workspaceDelete,
	"client_usage_daily":                 workspaceDeleteDetach,
	"comment":                            workspaceDelete,
	"comment_reaction":                   workspaceDelete,
	"contact_sales_inquiry":              workspaceDeleteKeep,
	"credit_balance":                     workspaceDeleteKeep,
	"credit_ledger":                      workspaceDeleteDetach,
	"daemon_connection":                  workspaceDelete,
	"daemon_token":                       workspaceDelete,
	"dingtalk_group_presence":            workspaceDelete,
	"dingtalk_bot_identity":              workspaceDelete,
	"dingtalk_group_route":               workspaceDelete,
	"feedback":                           workspaceDeleteDetach,
	"github_installation":                workspaceDelete,
	"github_pending_check_suite":         workspaceDelete,
	"github_pending_installation":        workspaceDeleteKeep,
	"github_pull_request":                workspaceDelete,
	"github_pull_request_check_run":      workspaceDelete,
	"github_pull_request_check_suite":    workspaceDelete,
	"inbox_item":                         workspaceDelete,
	"instance_telemetry_state":           workspaceDeleteKeep,
	"issue":                              workspaceDelete,
	"issue_wakeup":                       workspaceDelete,
	"issue_wakeup_receipt":               workspaceDelete,
	"issue_child_event":                  workspaceDelete,
	"issue_view":                         workspaceDelete,
	"issue_view_preference":              workspaceDelete,
	"issue_dependency":                   workspaceDelete,
	"issue_label":                        workspaceDelete,
	"issue_property":                     workspaceDelete,
	"issue_pr_automation":                workspaceDelete,
	"issue_pull_request":                 workspaceDelete,
	"issue_pull_request_exclusion":       workspaceDelete,
	"issue_reaction":                     workspaceDelete,
	"issue_source_context":               workspaceDelete,
	"issue_source_context_object_intent": workspaceDeleteSettle,
	"issue_status":                       workspaceDelete,
	"issue_subscriber":                   workspaceDelete,
	"issue_to_label":                     workspaceDelete,
	"issue_vcs_pull_request":             workspaceDelete,
	"lark_binding_token":                 workspaceDelete,
	"lark_chat_session_binding":          workspaceDelete,
	"lark_inbound_audit":                 workspaceDelete,
	"lark_inbound_message_dedup":         workspaceDelete,
	"lark_installation":                  workspaceDelete,
	"lark_outbound_card_message":         workspaceDelete,
	"lark_user_binding":                  workspaceDelete,
	"maintenance_job":                    workspaceDeleteKeep, // Global maintenance audit history, not workspace-owned.
	"member":                             workspaceDelete,
	"agent_mcp_server":                   workspaceDelete,
	"workspace_mcp_server":               workspaceDelete,
	"notification_preference":            workspaceDelete,
	"personal_access_token":              workspaceDeleteKeep,
	"pinned_item":                        workspaceDelete,
	"plugin_installation":                workspaceDelete,
	"plugin_hook_schedule":               workspaceDelete,
	"plugin_invocation":                  workspaceDelete,
	"plugin_storage":                     workspaceDelete,
	"plugin_secret":                      workspaceDelete,
	"plugin_package":                     workspaceDelete,
	"plugin_package_version":             workspaceDelete,
	"plugin_package_file":                workspaceDelete,
	"project":                            workspaceDelete,
	"project_resource":                   workspaceDelete,
	"quick_action":                       workspaceDelete,
	"runtime_profile":                    workspaceDelete,
	"schema_migrations":                  workspaceDeleteKeep,
	"search_index_change":                workspaceDelete,
	"search_index_prune_mark":            workspaceDeleteKeep,
	"seat_capacity_outbox":               workspaceDeleteSettle,
	"skill":                              workspaceDelete,
	"skill_file":                         workspaceDelete,
	"skill_to_label":                     workspaceDelete,
	"squad":                              workspaceDelete,
	"squad_member":                       workspaceDelete,
	"sys_cron_executions":                workspaceDeleteKeep,
	"task_message":                       workspaceDelete,
	"task_supplement":                    workspaceDelete,
	"task_supplement_capability":         workspaceDelete,
	"task_token":                         workspaceDelete,
	"task_usage":                         workspaceDelete,
	"task_usage_hourly":                  workspaceDelete,
	"task_usage_hourly_dirty":            workspaceDelete,
	"task_usage_hourly_rollup_state":     workspaceDeleteKeep,
	"user":                               workspaceDeleteKeep,
	"user_composio_connection":           workspaceDeleteKeep,
	"vcs_commit_status":                  workspaceDelete,
	"vcs_connection":                     workspaceDelete,
	"vcs_pull_request":                   workspaceDelete,
	"verification_code":                  workspaceDeleteKeep,
	"webhook_delivery":                   workspaceDelete,
	"workspace":                          workspaceDelete,
	"workspace_invitation":               workspaceDelete,
	"workspace_share_link":               workspaceDelete,

	// Fleet control-plane rows are deployment- or owner-scoped, not
	// workspace-owned: the namespace fence, credential profiles, node token
	// hashes and node operations carry no workspace_id and outlive any single
	// workspace.
	"fleet_credential_profiles": workspaceDeleteKeep,
	"fleet_namespace_fences":    workspaceDeleteKeep,
	"fleet_node_credentials":    workspaceDeleteKeep,
	"fleet_node_operations":     workspaceDeleteKeep,
	// An Aurora Fleet node carries the workspace_id/runtime_id dimensions, so
	// the workspace teardown must decide its fate. The Fleet control plane owns
	// the node's Docker container and volumes, which an API transaction cannot
	// clean up, so the row keeps its attribution and is handed to the Fleet
	// lifecycle reconciler instead of being deleted (or kept) here. That
	// handoff is real: DeleteWorkspace enqueues the Fleet destroy intent for
	// every workspace sandbox node before it deletes the aurora_sandbox_node
	// rows, so the reconciler can still reach the node and its owned Docker
	// resources. TestDeleteWorkspace_HandsSandboxNodesToFleet drives it.
	"fleet_nodes": workspaceDeleteSettle,
}

func TestWorkspaceDeletionManifestCoversPublicSchema(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}

	rows, err := testPool.Query(context.Background(), `
SELECT tablename
FROM pg_tables
WHERE schemaname = 'public'
ORDER BY tablename
`)
	if err != nil {
		t.Fatalf("list public tables: %v", err)
	}
	defer rows.Close()

	actual := make(map[string]struct{}, len(workspaceDeletionManifest))
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan public table: %v", err)
		}
		actual[table] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate public tables: %v", err)
	}

	var unclassified, missing []string
	for table := range actual {
		if _, ok := workspaceDeletionManifest[table]; !ok {
			unclassified = append(unclassified, table)
		}
	}
	for table := range workspaceDeletionManifest {
		if _, ok := actual[table]; !ok {
			missing = append(missing, table)
		}
	}
	sort.Strings(unclassified)
	sort.Strings(missing)
	if len(unclassified) > 0 || len(missing) > 0 {
		t.Fatalf("workspace deletion manifest drift: unclassified=%v missing=%v", unclassified, missing)
	}

	workspaceColumns, err := testPool.Query(context.Background(), `
SELECT table_name, is_nullable
FROM information_schema.columns
WHERE table_schema = 'public'
  AND column_name = 'workspace_id'
`)
	if err != nil {
		t.Fatalf("list workspace_id columns: %v", err)
	}
	defer workspaceColumns.Close()

	workspaceIDNullable := make(map[string]string)
	for workspaceColumns.Next() {
		var table, nullable string
		if err := workspaceColumns.Scan(&table, &nullable); err != nil {
			t.Fatalf("scan workspace_id table: %v", err)
		}
		workspaceIDNullable[table] = nullable
	}
	if err := workspaceColumns.Err(); err != nil {
		t.Fatalf("iterate workspace_id tables: %v", err)
	}

	for table, action := range workspaceDeletionManifest {
		nullable, hasWorkspaceID := workspaceIDNullable[table]
		switch action {
		case workspaceDeleteKeep:
			if hasWorkspaceID {
				t.Errorf("KEEP table %s gained workspace_id; classify its teardown behavior", table)
			}
		case workspaceDeleteDetach:
			// Detach is implemented by writing workspace_id = NULL, which a NOT
			// NULL column rejects — the classification is only implementable
			// while the column is nullable.
			if !hasWorkspaceID {
				t.Errorf("detach table %s lost workspace_id; update its teardown selector", table)
				continue
			}
			if nullable != "YES" {
				t.Errorf("detach table %s has a NOT NULL workspace_id; detaching it requires a nullable column", table)
			}
		case workspaceDeleteSettle:
			// Settle is not detach: the row keeps its attribution and is handed
			// to a reconciler, which is why every settle table's workspace_id is
			// NOT NULL (channel_media_pending_object is marked for deletion by
			// state, and the two outbox tables are drained by their own
			// reconcilers). fleet_nodes is settled through a real handoff:
			// DeleteWorkspace enqueues the Fleet destroy intent for each of the
			// workspace's sandbox nodes before deleting their rows, so the Fleet
			// reconciler can still reach the node (tested by
			// TestDeleteWorkspace_HandsSandboxNodesToFleet). Only the selector is
			// checked here.
			if !hasWorkspaceID {
				t.Errorf("settle table %s lost workspace_id; update its teardown selector", table)
			}
		}
	}
}
