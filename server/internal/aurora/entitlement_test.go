package aurora_test

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/aurora"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// entitlementTiers has empty price ids: the gates read plan numbers, never
// prices.
func entitlementTiers() *aurora.TierCatalog {
	return aurora.NewTierCatalog("", "", "", "", "", "", "")
}

func TestLimitsForUserDefaultsToFree(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	user := newAuroraTestUser(t, pool)
	tiers := entitlementTiers()
	free, ok := tiers.Lookup("free")
	if !ok {
		t.Fatal("free tier missing from the catalog")
	}

	// No subscription row at all.
	limits, err := aurora.LimitsForUser(context.Background(), q, tiers, user)
	if err != nil {
		t.Fatalf("LimitsForUser: %v", err)
	}
	if limits.Tier != "free" || limits.GenerationsPerMonth != free.GenerationsPerMonth || limits.Concurrency != free.Concurrency {
		t.Fatalf("limits without a subscription = %+v, want the free tier %+v", limits, free)
	}

	// A canceled subscription is not an active one: the user is on free limits,
	// not on the plan they stopped paying for.
	seedSubscription(t, pool, user, "pro", "canceled")
	limits, err = aurora.LimitsForUser(context.Background(), q, tiers, user)
	if err != nil {
		t.Fatalf("LimitsForUser with a canceled subscription: %v", err)
	}
	if limits.Tier != "free" {
		t.Fatalf("limits for a canceled subscription = %+v, want the free tier", limits)
	}

	// Neither is a past-due one.
	seedSubscription(t, pool, user, "pro", "past_due")
	limits, err = aurora.LimitsForUser(context.Background(), q, tiers, user)
	if err != nil {
		t.Fatalf("LimitsForUser with a past-due subscription: %v", err)
	}
	if limits.Tier != "free" {
		t.Fatalf("limits for a past-due subscription = %+v, want the free tier", limits)
	}
}

func TestLimitsForUserReadsTheActiveSubscription(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	user := newAuroraTestUser(t, pool)
	tiers := entitlementTiers()
	pro, ok := tiers.Lookup("pro")
	if !ok {
		t.Fatal("pro tier missing from the catalog")
	}
	seedSubscription(t, pool, user, "pro", "active")

	limits, err := aurora.LimitsForUser(context.Background(), q, tiers, user)
	if err != nil {
		t.Fatalf("LimitsForUser: %v", err)
	}
	if limits.Tier != "pro" || limits.GenerationsPerMonth != pro.GenerationsPerMonth || limits.Concurrency != pro.Concurrency {
		t.Fatalf("limits = %+v, want the pro tier %+v", limits, pro)
	}
}

// A row naming a tier the catalog no longer has must not leave the user
// ungated: the fallback is the free tier, never "no limits".
func TestLimitsForUserFallsBackForAnUnknownTier(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	user := newAuroraTestUser(t, pool)
	tiers := entitlementTiers()
	seedSubscription(t, pool, user, "enterprise", "active")

	limits, err := aurora.LimitsForUser(context.Background(), q, tiers, user)
	if err != nil {
		t.Fatalf("LimitsForUser: %v", err)
	}
	free, _ := tiers.Lookup("free")
	if limits.Tier != "free" || limits.GenerationsPerMonth != free.GenerationsPerMonth || limits.Concurrency != free.Concurrency {
		t.Fatalf("limits for an unknown tier = %+v, want the free tier %+v", limits, free)
	}
}
