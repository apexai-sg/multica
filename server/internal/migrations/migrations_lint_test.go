package migrations

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// auroraMigrationReservedNumber is the first number reserved for Aurora (and
// other fork-only) migrations. The fork and upstream developed in parallel for
// months, so numbers from 129 upward stopped being globally unique. Renumbering
// an applied migration is not a fix: the runner keys schema_migrations on the
// full file stem, so a rename replays it. New Aurora migrations therefore take
// a number from this reserved range.
const auroraMigrationReservedNumber = 5000

// grandfatheredSharedPrefixes lists the numbers >= 129 that two parallel lines
// of work claimed before the reserved range existed. A number on this list may
// repeat; any other repeated number still fails, so a new collision cannot slip
// in unnoticed.
var grandfatheredSharedPrefixes = map[int]string{
	500: "500_aurora_generation / 500_task_message_call_id",
	501: "501_aurora_asset / 501_runtime_profile_runtime_type",
	502: "502_channel_reply_delivery / 502_credit_balance",
	503: "503_channel_reply_delivery_turn_index / 503_credit_ledger",
	504: "504_channel_reply_delivery_installation_index / 504_credit_ledger_idempotency_key_idx",
	505: "505_channel_reply_delivery_binding_index / 505_credit_ledger_user_created_at_idx",
	506: "506_aurora_generation_workspace_idx / 506_channel_reply_delivery_attempt_depth",
	509: "509_aurora_generation_task_idx / 509_issue_wakeup",
	510: "510_aurora_moderation_log / 510_wakeup_id",
	511: "511_aurora_moderation_log_workspace_idx / 511_wakeup_issue",
	512: "512_aurora_subscription / 512_wakeup_due",
	513: "513_aurora_subscription_user_idx / 513_wakeup_receipt_id",
	514: "514_aurora_subscription_status_period_idx / 514_wakeup_receipt_key",
	515: "515_aurora_subscription_checkout_state / 515_wakeup_receipt_pending",
	516: "516_aurora_signup_bonus / 516_wakeup_pending_scope",
	518: "518_aurora_sandbox_node / 518_wakeup_event_capture",
	519: "519_aurora_sandbox_node_id_idx / 519_wakeup_event_issue",
	520: "520_aurora_sandbox_node_primary_key / 520_collaboration_wakeup_events",
	521: "521_aurora_sandbox_node_workspace_idx / 521_wakeup_workspace_summary",
	522: "522_aurora_sandbox_node_runtime_idx / 522_wakeup_run_lookup",
	523: "523_aurora_sandbox_node_daemon_idx / 523_wakeup_registration_source",
	524: "524_aurora_sandbox_node_enrollment_idx / 524_wakeup_workspace_history",
	525: "525_aurora_sandbox_node_reap_idx / 525_wakeup_active_runs",
	526: "526_aurora_managed_runtime_workspace_idx / 526_wakeup_terminal_runs",
	527: "527_aurora_provider_run / 527_wakeup_receipt_expiry",
	528: "528_aurora_provider_run_id_idx / 528_wakeup_receipt_coalescing",
	529: "529_aurora_provider_run_primary_key / 529_wakeup_pending_event",
	530: "530_aurora_provider_run_task_operation_idx / 530_wakeup_bounded_capture",
	531: "531_aurora_provider_run_external_idx / 531_wakeup_actor_filter",
	532: "532_aurora_artifact_staging / 532_wakeup_actor_capture",
	533: "533_aurora_artifact_staging_id_idx / 533_wakeup_close_in_app",
	534: "534_aurora_artifact_staging_primary_key / 534_drop_comment_agent_delivery",
	535: "535_aurora_artifact_staging_task_artifact_idx / 535_github_pr_address_index",
	536: "536_aurora_asset_metadata / 536_issue_duplicate_of",
	537: "537_aurora_asset_manifest_idx / 537_issue_duplicate_of_index",
	564: "564_fleet_tables / 564_issue_property_list_types",
	565: "565_channel_typing_cleanup / 565_fleet_nodes_id_index",
	566: "566_channel_typing_reaction_id_idx / 566_fleet_nodes_daemon_index",
	567: "567_channel_typing_reaction_retry_idx / 567_fleet_operations_id_index",
	568: "568_channel_typing_reaction_gc_idx / 568_fleet_operations_idempotency_index",
	569: "569_channel_typing_limits / 569_fleet_credentials_id_index",
	570: "570_channel_typing_quota_idx / 570_fleet_credentials_hash_index",
	571: "571_channel_typing_expiry_idx / 571_fleet_profiles_id_index",
	572: "572_channel_typing_abandoned_idx / 572_fleet_profiles_owner_index",
	573: "573_fleet_nodes_owner_index / 573_issue_workflow",
	574: "574_fleet_operations_pending_index / 574_issue_workflow_pkey_index",
	575: "575_fleet_runtime_node_index / 575_issue_workflow_primary_key",
	576: "576_fleet_provisioning_snapshot / 576_issue_workflow_workspace_name_index",
	577: "577_fleet_recovery_fences / 577_project_workflow_id",
	578: "578_agent_task_workflow_step / 578_fleet_lifecycle_dispatch",
	579: "579_fleet_namespace_fences / 579_wakeup_skip_handed_to_agent",
	581: "581_fleet_namespace_finalized / 581_issue_workflow_is_default",
	582: "582_fleet_namespace_manifest / 582_issue_workflow_default_index",
	583: "583_fleet_nodes_aurora_dimensions / 583_runtime_profile_add_muse",
	584: "584_fleet_nodes_workspace_index / 584_quick_action_optional_target",
	585: "585_aurora_agents_visible / 585_issue_workflow_owner",
	586: "586_aurora_generation_issue / 586_cloud_agent_connection",
	587: "587_aurora_generation_issue_idx / 587_cloud_agent_connection_provider_index",
	588: "588_aurora_asset_media_url_idx / 588_daemon_token_owner",
}

// forkLegacyPrefixes are the fork's own migrations that shipped before the
// reserved range existed. They are applied on live databases, so they keep
// their numbers; only new fork migrations must use the reserved range.
var forkLegacyPrefixes = map[int]bool{
	// Aurora.
	500: true, 501: true, 506: true, 507: true, 508: true, 509: true,
	510: true, 511: true, 512: true, 513: true, 514: true, 515: true,
	516: true, 517: true, 518: true, 519: true, 520: true, 521: true,
	522: true, 523: true, 524: true, 525: true, 526: true, 527: true,
	528: true, 529: true, 530: true, 531: true, 532: true, 533: true,
	534: true, 535: true, 536: true, 537: true,
	// Fleet.
	564: true, 565: true, 566: true, 567: true, 568: true, 569: true, 570: true, 571: true, 572: true,
	573: true, 574: true, 575: true, 576: true, 577: true, 578: true, 579: true, 580: true, 581: true,
	582: true, 583: true, 584: true,
	// Aurora after the Fleet range.
	585: true, 586: true, 587: true, 588: true,
}

func TestMigrationNumericPrefixesAreUnique(t *testing.T) {
	files := migrationFilesForLint(t, "*.up.sql")

	// Migrations through 128 contain historical duplicate numeric prefixes.
	// From 129 onward, keep the numeric sequence unique so release tooling and
	// operators can identify one schema change unambiguously by its number.
	const firstUniqueMigrationNumber = 129
	stemByNumber := make(map[int]string)
	for _, file := range files {
		stem, _, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		prefix, _, ok := strings.Cut(stem, "_")
		if !ok {
			continue
		}
		number, err := strconv.Atoi(prefix)
		if err != nil || number < firstUniqueMigrationNumber {
			continue
		}
		if previous, exists := stemByNumber[number]; exists {
			if _, grandfathered := grandfatheredSharedPrefixes[number]; grandfathered {
				// This number was claimed twice before the fork reserved its own
				// range. Both migrations are applied and recorded under their full
				// stems, so the collision is historical, not a new one.
				continue
			}
			t.Errorf("migrations %s and %s share numeric prefix %s", previous, stem, prefix)
			continue
		}
		stemByNumber[number] = stem
	}
}

// TestGrandfatheredPrefixesStillCollide keeps the grandfather list honest: once
// one side of a historical collision disappears (for example a deleted or
// renumbered migration), the exemption must go too, or it would silently allow
// a future duplicate to reuse that number.
func TestGrandfatheredPrefixesStillCollide(t *testing.T) {
	files := migrationFilesForLint(t, "*.up.sql")
	count := map[int]int{}
	for _, file := range files {
		stem, _, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		prefix, _, ok := strings.Cut(stem, "_")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(prefix); err == nil {
			count[n]++
		}
	}
	for number := range grandfatheredSharedPrefixes {
		if count[number] < 2 {
			t.Errorf("grandfathered prefix %d no longer collides; remove it", number)
		}
	}
}

// TestAuroraMigrationsUseTheReservedRange keeps the fork's own migrations from
// colliding with upstream again. Aurora and Fleet are fork-only, so they own the
// reserved range; the migrations that predate the range are listed explicitly.
func TestAuroraMigrationsUseTheReservedRange(t *testing.T) {
	files := migrationFilesForLint(t, "*.up.sql")

	for _, file := range files {
		stem, _, ok := splitMigrationFilename(filepath.Base(file))
		if !ok || !(strings.Contains(stem, "aurora") || strings.Contains(stem, "fleet")) {
			continue
		}
		prefix, _, ok := strings.Cut(stem, "_")
		if !ok {
			continue
		}
		number, err := strconv.Atoi(prefix)
		if err != nil {
			continue
		}
		if number >= auroraMigrationReservedNumber || forkLegacyPrefixes[number] {
			continue
		}
		t.Errorf("fork migration %s must use a number >= %d", stem, auroraMigrationReservedNumber)
	}
}

func TestMigrationFilesHaveMatchingDirections(t *testing.T) {
	files := migrationFilesForLint(t, "*.sql")

	directionsByStem := make(map[string]map[string]bool)
	for _, file := range files {
		stem, direction, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		if directionsByStem[stem] == nil {
			directionsByStem[stem] = make(map[string]bool)
		}
		directionsByStem[stem][direction] = true
	}

	for stem, directions := range directionsByStem {
		if !directions["up"] || !directions["down"] {
			t.Errorf("migration %s must have both .up.sql and .down.sql files", stem)
		}
	}
}

func migrationFilesForLint(t *testing.T, pattern string) []string {
	t.Helper()

	dir := realMigrationsDir(t)
	files, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no migration files matched %s in %s", pattern, dir)
	}
	sort.Strings(files)
	return files
}

func realMigrationsDir(t *testing.T) string {
	t.Helper()

	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration lint test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", "migrations"))
}

func splitMigrationFilename(name string) (stem, direction string, ok bool) {
	for _, candidateDirection := range []string{"up", "down"} {
		suffix := fmt.Sprintf(".%s.sql", candidateDirection)
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix), candidateDirection, true
		}
	}
	return "", "", false
}
