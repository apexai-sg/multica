package aurora

// Tier is one personal subscription tier. MonthlyCredits is granted per
// natural month (the free tier lazily on the first generation of the month,
// see entitlement.go) and the unused remainder expires at the end of that
// month (settlement.go). GenerationsPerMonth and Concurrency are the local
// entitlement gates; all values are product-configurable constants, so this
// file is the single place a plan's numbers change.
type Tier struct {
	Tier                string
	MonthlyCredits      int64 // credits, 1 credit = 1e6 micro
	GenerationsPerMonth int
	Concurrency         int
	StripePriceMonthly  string
	StripePriceYearly   string
	// AntomAmountMinorMonthly is the monthly price as the integer string in the
	// currency's smallest unit that Antom charges ("1900" is S$19.00), and it is
	// the only amount the Antom checkout sends. A decimal string here would
	// charge a hundredth of the price.
	AntomAmountMinorMonthly string
}

// AntomCurrency is the currency the Antom channel charges in. The tier
// catalog's Antom amounts are expressed in its smallest unit.
const AntomCurrency = "SGD"

func (t Tier) MonthlyCreditsMicro() int64 { return t.MonthlyCredits * 1_000_000 }

// Topup is a one-time credit purchase. Topups never expire, which is why the
// monthly settlement excludes them from the expiry sum.
type Topup struct {
	ID      string // t5 | t20
	Credits int64
	PriceID string
}

// TierCatalog is the static product catalog. Price IDs come from env because
// they differ between Stripe test and live modes; an empty price ID makes the
// corresponding checkout fail closed (503) rather than charge a wrong price.
type TierCatalog struct {
	Tiers  []Tier // free, creator, pro, business — fixed order
	Topups []Topup
}

// Yearly billing is not sold in this launch, so the business tier has no yearly
// price id; its yearly checkout therefore fails closed like any unconfigured
// price.
func NewTierCatalog(creatorMonthly, creatorYearly, proMonthly, businessMonthly, proYearly, topup5, topup20 string) *TierCatalog {
	return &TierCatalog{
		Tiers: []Tier{
			{Tier: "free", MonthlyCredits: 200, GenerationsPerMonth: 10, Concurrency: 1},
			{Tier: "creator", MonthlyCredits: 1900, GenerationsPerMonth: 30, Concurrency: 2, StripePriceMonthly: creatorMonthly, StripePriceYearly: creatorYearly, AntomAmountMinorMonthly: "1900"},
			{Tier: "pro", MonthlyCredits: 5900, GenerationsPerMonth: 100, Concurrency: 5, StripePriceMonthly: proMonthly, StripePriceYearly: proYearly, AntomAmountMinorMonthly: "5900"},
			{Tier: "business", MonthlyCredits: 16900, GenerationsPerMonth: 300, Concurrency: 10, StripePriceMonthly: businessMonthly, AntomAmountMinorMonthly: "16900"},
		},
		Topups: []Topup{
			{ID: "t5", Credits: 500, PriceID: topup5},
			{ID: "t20", Credits: 2000, PriceID: topup20},
		},
	}
}

func (c *TierCatalog) Lookup(tier string) (Tier, bool) {
	for _, t := range c.Tiers {
		if t.Tier == tier {
			return t, true
		}
	}
	return Tier{}, false
}

func (c *TierCatalog) LookupTopup(id string) (Topup, bool) {
	for _, t := range c.Topups {
		if t.ID == id {
			return t, true
		}
	}
	return Topup{}, false
}

// FreeMonthlyMicro is the free tier's monthly grant, used by the lazy grant on
// a free user's first generation of the month.
func (c *TierCatalog) FreeMonthlyMicro() int64 {
	t, _ := c.Lookup("free")
	return t.MonthlyCreditsMicro()
}
