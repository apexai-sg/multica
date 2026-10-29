package aurora_test

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/aurora"
)

func TestTierCatalog(t *testing.T) {
	tc := aurora.NewTierCatalog("", "", "", "", "", "", "") // price ids empty in unit tests
	if len(tc.Tiers) != 4 {
		t.Fatalf("expected 4 tiers, got %d", len(tc.Tiers))
	}
	creator, ok := tc.Lookup("creator")
	if !ok {
		t.Fatal("creator tier missing")
	}
	if creator.MonthlyCredits != 1900 {
		t.Fatalf("creator monthly credits = %d, want 1900", creator.MonthlyCredits)
	}
	if creator.MonthlyCreditsMicro() != 1900*1_000_000 {
		t.Fatal("creator micro conversion wrong")
	}
	if creator.GenerationsPerMonth != 30 {
		t.Fatalf("creator generations/month = %d, want 30", creator.GenerationsPerMonth)
	}
	pro, _ := tc.Lookup("pro")
	if pro.MonthlyCredits != 5900 || pro.GenerationsPerMonth != 100 || pro.Concurrency != 5 {
		t.Fatalf("pro tier wrong: %+v", pro)
	}
	business, ok := tc.Lookup("business")
	if !ok {
		t.Fatal("business tier missing")
	}
	if business.MonthlyCredits != 16900 || business.GenerationsPerMonth != 300 || business.Concurrency != 10 {
		t.Fatalf("business tier wrong: %+v", business)
	}
	free, _ := tc.Lookup("free")
	if free.MonthlyCredits != 200 || free.Concurrency != 1 || free.GenerationsPerMonth != 10 {
		t.Fatalf("free tier wrong: %+v", free)
	}
	if len(tc.Topups) != 2 {
		t.Fatalf("expected 2 topup tiers, got %d", len(tc.Topups))
	}
	// Antom's amount is the integer string in the currency's smallest unit, so
	// the catalog stores S$19.00 as "1900" — a decimal string is a different
	// (100x smaller) charge, not a formatting choice.
	for name, want := range map[string]string{
		"creator": "1900", "pro": "5900", "business": "16900",
	} {
		tier, ok := tc.Lookup(name)
		if !ok {
			t.Fatalf("%s tier missing", name)
		}
		if tier.AntomAmountMinorMonthly != want {
			t.Errorf("%s Antom monthly amount = %q, want %q", name, tier.AntomAmountMinorMonthly, want)
		}
	}
	if free, _ := tc.Lookup("free"); free.AntomAmountMinorMonthly != "" {
		t.Errorf("free tier has an Antom amount: %q", free.AntomAmountMinorMonthly)
	}
	// Top-ups stay at 100 credits per SGD: S$5 and S$20.
	for id, credits := range map[string]int64{"t5": 500, "t20": 2000} {
		topup, ok := tc.LookupTopup(id)
		if !ok {
			t.Fatalf("topup %q missing", id)
		}
		if topup.Credits != credits {
			t.Errorf("topup %q credits = %d, want %d", id, topup.Credits, credits)
		}
	}
}

// The paid tiers must be strictly ordered, or a tier comparison anywhere in the
// entitlement path would be meaningless. The free tier must be the cheapest of
// all four on every axis, since it is the fallback LimitsForUser hands a user
// with no subscription.
func TestTierCatalogIsMonotonic(t *testing.T) {
	tc := aurora.NewTierCatalog("cm", "cy", "pm", "bm", "py", "t5", "t20")
	prev, ok := tc.Lookup("free")
	if !ok {
		t.Fatal("free tier missing")
	}
	for _, name := range []string{"creator", "pro", "business"} {
		cur, ok := tc.Lookup(name)
		if !ok {
			t.Fatalf("%s tier missing", name)
		}
		if cur.MonthlyCredits <= prev.MonthlyCredits {
			t.Errorf("%s monthly credits %d not above %s's %d", name, cur.MonthlyCredits, prev.Tier, prev.MonthlyCredits)
		}
		if cur.GenerationsPerMonth <= prev.GenerationsPerMonth {
			t.Errorf("%s generations/month %d not above %s's %d", name, cur.GenerationsPerMonth, prev.Tier, prev.GenerationsPerMonth)
		}
		if cur.Concurrency <= prev.Concurrency {
			t.Errorf("%s concurrency %d not above %s's %d", name, cur.Concurrency, prev.Tier, prev.Concurrency)
		}
		prev = cur
	}
	// The paid tiers carry the price ids the catalog was built with; the free
	// tier must not, because a free checkout is a bug rather than a purchase.
	if free, _ := tc.Lookup("free"); free.StripePriceMonthly != "" || free.StripePriceYearly != "" {
		t.Errorf("free tier has a price id: %+v", free)
	}
	for _, name := range []string{"creator", "pro"} {
		tier, _ := tc.Lookup(name)
		if tier.StripePriceMonthly == "" || tier.StripePriceYearly == "" {
			t.Errorf("%s tier lost its price ids: %+v", name, tier)
		}
	}
	// Yearly billing is not sold in this launch, so the business tier is
	// monthly-only; an accidental yearly id would enable a checkout whose price
	// this launch never decided.
	business, _ := tc.Lookup("business")
	if business.StripePriceMonthly != "bm" {
		t.Errorf("business monthly price = %q, want bm", business.StripePriceMonthly)
	}
	if business.StripePriceYearly != "" {
		t.Errorf("business tier has a yearly price id: %+v", business)
	}
}

func TestTierCatalogUnknownLookups(t *testing.T) {
	tc := aurora.NewTierCatalog("", "", "", "", "", "", "")
	if _, ok := tc.Lookup("enterprise"); ok {
		t.Error("Lookup returned an unknown tier")
	}
	if _, ok := tc.Lookup(""); ok {
		t.Error("Lookup returned the empty tier")
	}
	if _, ok := tc.LookupTopup("t50"); ok {
		t.Error("LookupTopup returned an unknown topup")
	}
}
