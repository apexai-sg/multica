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
}

// auroraLegacyPrefixes are the Aurora migrations that shipped before the
// reserved range existed. They are applied on live databases, so they keep
// their numbers; only new Aurora migrations must use the reserved range.
var auroraLegacyPrefixes = map[int]bool{500: true, 501: true, 506: true, 507: true, 508: true, 509: true, 510: true, 511: true, 512: true, 513: true, 514: true, 515: true, 516: true, 517: true, 518: true, 519: true, 520: true, 521: true, 522: true, 523: true, 524: true, 525: true, 526: true, 527: true, 528: true, 529: true, 530: true, 531: true, 532: true, 533: true, 534: true, 535: true, 536: true, 537: true, 583: true, 585: true, 586: true, 587: true, 588: true}

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

// TestAuroraMigrationsUseTheReservedRange keeps the fork's own migrations from
// colliding with upstream again. Aurora is fork-only, so it owns the reserved
// range; the migrations that predate the range are listed explicitly.
func TestAuroraMigrationsUseTheReservedRange(t *testing.T) {
	files := migrationFilesForLint(t, "*.up.sql")

	for _, file := range files {
		stem, _, ok := splitMigrationFilename(filepath.Base(file))
		if !ok || !strings.Contains(stem, "aurora") {
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
		if number >= auroraMigrationReservedNumber || auroraLegacyPrefixes[number] {
			continue
		}
		t.Errorf("Aurora migration %s must use a number >= %d", stem, auroraMigrationReservedNumber)
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
