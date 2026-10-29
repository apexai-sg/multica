package aurora_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// settlementTiers has empty price ids: the settlement reads plan numbers, never
// prices, so a test never needs a Stripe price.
func settlementTiers() *aurora.TierCatalog {
	return aurora.NewTierCatalog("", "", "", "", "", "", "")
}

// naturalMonthStarts returns the UTC boundaries the settlement must use. Tests
// seed inside the previous calendar month rather than inside a rolling
// `now-1 month` window, because the production loop runs every day.
func naturalMonthStarts(now time.Time) (previous, current time.Time) {
	now = now.UTC()
	current = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return current.AddDate(0, -1, 0), current
}

// seedMonthlyGrant writes the "sub:<userID>:<YYYY-MM>" adjustment a grant
// produces, at a chosen time. The reference shape is the contract the expiry
// phase keys on (reference LIKE 'sub:%').
func seedMonthlyGrant(t *testing.T, pool *pgxpool.Pool, userID, workspaceID pgtype.UUID, amountMicro int64, at time.Time) {
	t.Helper()
	reference := "sub:" + util.UUIDToString(userID) + ":" + at.Format("2006-01")
	seedCreditLedgerRow(t, pool, userID, workspaceID, aurora.LedgerKindAdjustment, amountMicro, reference,
		"seed-grant-"+uuid.NewString(), at)
}

// seedSubscription gives a user an active plan whose period has not ended, which
// is what the grant phase selects on.
func seedSubscription(t *testing.T, pool *pgxpool.Pool, userID pgtype.UUID, tier, status string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO aurora_subscription (user_id, tier, status, current_period_end)
		VALUES ($1, $2, $3, now() + interval '30 days')
		ON CONFLICT (user_id) DO UPDATE SET
			tier = EXCLUDED.tier, status = EXCLUDED.status,
			current_period_end = EXCLUDED.current_period_end, updated_at = now()`,
		userID, tier, status); err != nil {
		t.Fatalf("seed aurora_subscription: %v", err)
	}
}

// monthlyGrantSumSince totals the monthly ("sub:") grants a user received at or
// after since — the grant phase's output, isolated from the expiry phase's rows.
func monthlyGrantSumSince(t *testing.T, pool *pgxpool.Pool, userID pgtype.UUID, since time.Time) int64 {
	t.Helper()
	var sum int64
	if err := pool.QueryRow(context.Background(), `
		SELECT coalesce(sum(amount_micro), 0) FROM credit_ledger
		WHERE user_id = $1 AND kind = 'adjustment' AND reference LIKE 'sub:%' AND created_at >= $2`,
		userID, since).Scan(&sum); err != nil {
		t.Fatalf("sum monthly grants: %v", err)
	}
	return sum
}

func TestRunMonthlySettlementExpiresAndGrants(t *testing.T) {
	svc, pool := newTestCreditService(t)
	user := newAuroraTestUser(t, pool)
	ws := newAuroraTestWorkspace(t, pool, user)
	now := time.Now().UTC().Truncate(time.Second)
	windowStart, _ := naturalMonthStarts(now)

	// Last month: a 1000-micro grant, 600 spent, so 400 is left over.
	seedMonthlyGrant(t, pool, user, ws, 1000, windowStart.Add(time.Hour))
	seedCreditLedgerRow(t, pool, user, ws, aurora.LedgerKindDeduction, -600,
		"gen-"+uuid.NewString(), "seed-spend-"+uuid.NewString(), windowStart.Add(2*time.Hour))
	seedCreditBalance(t, pool, user, 400)
	seedSubscription(t, pool, user, "creator", "active")

	if err := aurora.RunMonthlySettlement(context.Background(), db.New(pool), svc, settlementTiers(), now); err != nil {
		t.Fatalf("RunMonthlySettlement: %v", err)
	}

	count, sum := ledgerCountAndSum(t, pool, user, aurora.LedgerKindExpire)
	if count != 1 || sum != -400 {
		t.Fatalf("expire rows = %d summing %d, want 1 row summing -400", count, sum)
	}
	if granted := monthlyGrantSumSince(t, pool, user, now.Add(-time.Minute)); granted != 1900*1_000_000 {
		t.Fatalf("this month's grant = %d, want %d", granted, 1900*1_000_000)
	}
	// 400 expired from a wallet holding exactly the leftover, then one month of
	// creator credits.
	if bal := creditBalanceOf(t, pool, user); bal != 1900*1_000_000 {
		t.Fatalf("balance = %d, want %d", bal, 1900*1_000_000)
	}
}

func TestRunMonthlySettlementUsesNaturalMonthBoundaries(t *testing.T) {
	svc, pool := newTestCreditService(t)
	user := newAuroraTestUser(t, pool)
	ws := newAuroraTestWorkspace(t, pool, user)
	now := time.Now().UTC().Truncate(time.Second)
	previousStart, currentStart := naturalMonthStarts(now)

	// The previous calendar month has 600 micro left. This month already has a
	// separate 700-micro grant. A daily settlement run in the middle of the
	// month must expire only the 600; a rolling now-1-month window would include
	// the current grant and expire it early.
	seedMonthlyGrant(t, pool, user, ws, 1000, previousStart.Add(time.Hour))
	seedCreditLedgerRow(t, pool, user, ws, aurora.LedgerKindDeduction, -400,
		"gen-"+uuid.NewString(), "seed-spend-"+uuid.NewString(), previousStart.Add(2*time.Hour))
	seedMonthlyGrant(t, pool, user, ws, 700, currentStart.Add(time.Hour))
	seedCreditBalance(t, pool, user, 1300)

	if err := aurora.RunMonthlySettlement(context.Background(), db.New(pool), svc, settlementTiers(), now); err != nil {
		t.Fatalf("RunMonthlySettlement: %v", err)
	}

	if count, sum := ledgerCountAndSum(t, pool, user, aurora.LedgerKindExpire); count != 1 || sum != -600 {
		t.Fatalf("expire rows = %d summing %d, want 1 row summing -600", count, sum)
	}
	if bal := creditBalanceOf(t, pool, user); bal != 700 {
		t.Fatalf("balance = %d, want this month's 700-micro grant untouched", bal)
	}
}

// The loop is not bound to the 1st: it runs on every tick, and a tick that
// lands on the 2nd (a restart, a missed schedule) still settles.
func TestRunMonthlySettlementCatchUpAfterMissedFirst(t *testing.T) {
	svc, pool := newTestCreditService(t)
	user := newAuroraTestUser(t, pool)
	ws := newAuroraTestWorkspace(t, pool, user)
	// The clock the loop runs at is a day past the 1st. The rows it writes are
	// stamped with the database's clock, not this one, so the assertions below
	// bound the grant window by the real clock, not by `now`.
	_, currentMonthStart := naturalMonthStarts(time.Now())
	now := currentMonthStart.Add(36 * time.Hour) // the 2nd at noon UTC
	grantedSince := time.Now().UTC().Add(-time.Minute)
	windowStart, _ := naturalMonthStarts(now)

	seedMonthlyGrant(t, pool, user, ws, 1000, windowStart.Add(time.Hour))
	seedCreditLedgerRow(t, pool, user, ws, aurora.LedgerKindDeduction, -600,
		"gen-"+uuid.NewString(), "seed-spend-"+uuid.NewString(), windowStart.Add(2*time.Hour))
	seedCreditBalance(t, pool, user, 400)
	seedSubscription(t, pool, user, "creator", "active")

	if err := aurora.RunMonthlySettlement(context.Background(), db.New(pool), svc, settlementTiers(), now); err != nil {
		t.Fatalf("RunMonthlySettlement: %v", err)
	}

	if count, sum := ledgerCountAndSum(t, pool, user, aurora.LedgerKindExpire); count != 1 || sum != -400 {
		t.Fatalf("expire rows = %d summing %d, want 1 row summing -400", count, sum)
	}
	if granted := monthlyGrantSumSince(t, pool, user, grantedSince); granted != 1900*1_000_000 {
		t.Fatalf("grant = %d, want %d", granted, 1900*1_000_000)
	}
}

// The signup bonus and topups never expire: the expiry phase sums only "sub:"
// grants, so a user whose whole balance came from a bonus and a purchase is not
// touched at all.
func TestRunMonthlySettlementDoesNotExpireSignupBonusOrTopup(t *testing.T) {
	svc, pool := newTestCreditService(t)
	user := newAuroraTestUser(t, pool)
	ws := newAuroraTestWorkspace(t, pool, user)
	now := time.Now().UTC().Truncate(time.Second)
	windowStart, _ := naturalMonthStarts(now)

	seedCreditLedgerRow(t, pool, user, ws, aurora.LedgerKindAdjustment, 500,
		"signup:"+util.UUIDToString(user), "seed-signup-"+uuid.NewString(), windowStart.Add(time.Hour))
	seedCreditLedgerRow(t, pool, user, ws, aurora.LedgerKindTopup, 2000,
		"evt_topup_"+uuid.NewString(), "seed-topup-"+uuid.NewString(), windowStart.Add(2*time.Hour))
	seedCreditBalance(t, pool, user, 2500)

	if err := aurora.RunMonthlySettlement(context.Background(), db.New(pool), svc, settlementTiers(), now); err != nil {
		t.Fatalf("RunMonthlySettlement: %v", err)
	}

	if count, sum := ledgerCountAndSum(t, pool, user, aurora.LedgerKindExpire); count != 0 || sum != 0 {
		t.Fatalf("expire rows = %d summing %d, want none", count, sum)
	}
	if bal := creditBalanceOf(t, pool, user); bal != 2500 {
		t.Fatalf("balance = %d, want 2500 (bonus and topup are never expired)", bal)
	}
}

// One broken user must not stop the run: a user with a monthly grant but no
// personal workspace is skipped with a warning, and the healthy user is still
// settled.
func TestRunMonthlySettlementSkipsFailingUser(t *testing.T) {
	svc, pool := newTestCreditService(t)
	healthy := newAuroraTestUser(t, pool)
	healthyWS := newAuroraTestWorkspace(t, pool, healthy)
	broken := newAuroraTestUser(t, pool) // no workspace, so no owner membership
	now := time.Now().UTC().Truncate(time.Second)
	windowStart, _ := naturalMonthStarts(now)

	seedMonthlyGrant(t, pool, healthy, healthyWS, 1000, windowStart.Add(time.Hour))
	seedCreditBalance(t, pool, healthy, 1000)
	seedMonthlyGrant(t, pool, broken, pgtype.UUID{}, 1000, windowStart.Add(time.Hour))
	seedCreditBalance(t, pool, broken, 1000)
	seedSubscription(t, pool, healthy, "creator", "active")

	if err := aurora.RunMonthlySettlement(context.Background(), db.New(pool), svc, settlementTiers(), now); err != nil {
		t.Fatalf("RunMonthlySettlement returned an error for one broken user: %v", err)
	}

	if count, sum := ledgerCountAndSum(t, pool, healthy, aurora.LedgerKindExpire); count != 1 || sum != -1000 {
		t.Fatalf("healthy user expire rows = %d summing %d, want 1 row summing -1000", count, sum)
	}
	if count, _ := ledgerCountAndSum(t, pool, broken, aurora.LedgerKindExpire); count != 0 {
		t.Fatalf("broken user has %d expire rows, want 0 (skipped)", count)
	}
}

// Rerunning is the loop's normal case, not an edge case: every write is keyed
// on the month (or the expiry reference), so a second run in the same month
// changes nothing.
func TestRunMonthlySettlementIdempotent(t *testing.T) {
	svc, pool := newTestCreditService(t)
	user := newAuroraTestUser(t, pool)
	ws := newAuroraTestWorkspace(t, pool, user)
	now := time.Now().UTC().Truncate(time.Second)
	windowStart, _ := naturalMonthStarts(now)

	seedMonthlyGrant(t, pool, user, ws, 1000, windowStart.Add(time.Hour))
	seedCreditBalance(t, pool, user, 1000)
	seedSubscription(t, pool, user, "creator", "active")

	q := db.New(pool)
	if err := aurora.RunMonthlySettlement(context.Background(), q, svc, settlementTiers(), now); err != nil {
		t.Fatalf("first RunMonthlySettlement: %v", err)
	}
	balanceAfterFirst := creditBalanceOf(t, pool, user)
	expireCount, expireSum := ledgerCountAndSum(t, pool, user, aurora.LedgerKindExpire)
	if expireCount != 1 || expireSum != -1000 {
		t.Fatalf("first run expired %d rows summing %d, want 1 row summing -1000", expireCount, expireSum)
	}

	if err := aurora.RunMonthlySettlement(context.Background(), q, svc, settlementTiers(), now); err != nil {
		t.Fatalf("second RunMonthlySettlement: %v", err)
	}
	if balanceAfterSecond := creditBalanceOf(t, pool, user); balanceAfterSecond != balanceAfterFirst {
		t.Fatalf("second run changed the balance: before=%d after=%d", balanceAfterFirst, balanceAfterSecond)
	}
	secondCount, secondSum := ledgerCountAndSum(t, pool, user, aurora.LedgerKindExpire)
	if secondCount != expireCount || secondSum != expireSum {
		t.Fatalf("second run changed the expiry: rows %d->%d sum %d->%d", expireCount, secondCount, expireSum, secondSum)
	}
}

// A canceled subscription stops future grants: the grant phase selects on
// status = 'active' only.
func TestRunMonthlySettlementSkipsCanceledSubscription(t *testing.T) {
	svc, pool := newTestCreditService(t)
	user := newAuroraTestUser(t, pool)
	now := time.Now().UTC().Truncate(time.Second)

	seedSubscription(t, pool, user, "creator", "canceled")

	if err := aurora.RunMonthlySettlement(context.Background(), db.New(pool), svc, settlementTiers(), now); err != nil {
		t.Fatalf("RunMonthlySettlement: %v", err)
	}
	if granted := monthlyGrantSumSince(t, pool, user, now.Add(-time.Minute)); granted != 0 {
		t.Fatalf("canceled subscription was granted %d, want 0", granted)
	}
}
