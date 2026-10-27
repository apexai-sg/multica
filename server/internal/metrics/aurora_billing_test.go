package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestAuroraBillingWebhookEventRecordsExactlyOneSeries pins the alerting
// contract: one webhook outcome increments one (event, result) series, so a
// failure cannot inflate an unrelated label.
func TestAuroraBillingWebhookEventRecordsExactlyOneSeries(t *testing.T) {
	m := NewBusinessMetrics()
	before := counterVecSum(t, m.auroraWebhookEvents)

	m.RecordAuroraWebhookEvent("customer.subscription.updated", AuroraWebhookResultHandlerError)

	const event = "customer_subscription_updated"
	if got := testutil.ToFloat64(m.auroraWebhookEvents.WithLabelValues(event, AuroraWebhookResultHandlerError)); got != 1 {
		t.Fatalf("handler_error series = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.auroraWebhookEvents.WithLabelValues(event, AuroraWebhookResultOK)); got != 0 {
		t.Fatalf("ok series = %v, want 0 (one outcome must not count as two)", got)
	}
	if got := testutil.ToFloat64(m.auroraWebhookEvents.WithLabelValues("checkout_session_completed", AuroraWebhookResultHandlerError)); got != 0 {
		t.Fatalf("other event series = %v, want 0", got)
	}
	if got := counterVecSum(t, m.auroraWebhookEvents); got != before+1 {
		t.Fatalf("family total = %v, want %v (exactly one series incremented)", got, before+1)
	}
}

// TestAuroraBillingWebhookLabelsAreBounded keeps the event and result labels on
// their closed enums: a Stripe type this product does not consume, a request
// rejected before verification, and an unclassified result must all fold onto
// existing labels rather than mint a new series.
func TestAuroraBillingWebhookLabelsAreBounded(t *testing.T) {
	m := NewBusinessMetrics()
	before := testutil.CollectAndCount(m.auroraWebhookEvents)

	m.RecordAuroraWebhookEvent("invoice.created", AuroraWebhookResultOK)
	m.RecordAuroraWebhookEvent(AuroraWebhookEventUnknown, AuroraWebhookResultNotConfigured)
	m.RecordAuroraWebhookEvent("customer.subscription.updated", "not-a-result")

	if got := testutil.ToFloat64(m.auroraWebhookEvents.WithLabelValues("other", AuroraWebhookResultOK)); got != 1 {
		t.Fatalf("unconsumed event series = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.auroraWebhookEvents.WithLabelValues(AuroraWebhookEventUnknown, AuroraWebhookResultNotConfigured)); got != 1 {
		t.Fatalf("unverified request series = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.auroraWebhookEvents.WithLabelValues("customer_subscription_updated", "unknown")); got != 1 {
		t.Fatalf("unclassified result series = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(m.auroraWebhookEvents); got != before {
		t.Fatalf("webhook series count = %d, want %d (labels stayed inside the enum)", got, before)
	}
}

// TestAuroraBillingPrewarmMatchesNormalizedLabels keeps the prewarmed zeros and
// the labels Record writes identical; otherwise the series a dashboard shows at
// zero is not the series an alert reads.
func TestAuroraBillingPrewarmMatchesNormalizedLabels(t *testing.T) {
	for _, event := range AllAuroraWebhookEvents() {
		if got := normalizeAuroraWebhookEvent(event); got != event {
			t.Errorf("normalizeAuroraWebhookEvent(%q) = %q, want itself", event, got)
		}
	}
	for _, result := range AllAuroraWebhookResults() {
		if got := normalizeAuroraWebhookResult(result); got != result {
			t.Errorf("normalizeAuroraWebhookResult(%q) = %q, want itself", result, got)
		}
	}
	for _, result := range AllAuroraSettlementResults() {
		if got := normalizeAuroraSettlementResult(result); got != result {
			t.Errorf("normalizeAuroraSettlementResult(%q) = %q, want itself", result, got)
		}
	}

	m := NewBusinessMetrics()
	wantWebhookSeries := len(AllAuroraWebhookEvents()) * len(AllAuroraWebhookResults())
	if got := testutil.CollectAndCount(m.auroraWebhookEvents); got != wantWebhookSeries {
		t.Fatalf("prewarmed webhook series = %d, want %d", got, wantWebhookSeries)
	}
	if got := testutil.CollectAndCount(m.auroraSettlementRuns); got != len(AllAuroraSettlementResults()) {
		t.Fatalf("prewarmed settlement series = %d, want %d", got, len(AllAuroraSettlementResults()))
	}
}

func TestAuroraBillingSettlementRunCountsPerOutcome(t *testing.T) {
	m := NewBusinessMetrics()

	m.RecordAuroraSettlementRun(AuroraSettlementResultOK)
	m.RecordAuroraSettlementRun(AuroraSettlementResultFailed)
	// An unclassified outcome is a failure: it is not evidence the run worked.
	m.RecordAuroraSettlementRun("partial")

	if got := testutil.ToFloat64(m.auroraSettlementRuns.WithLabelValues(AuroraSettlementResultOK)); got != 1 {
		t.Fatalf("settlement ok = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.auroraSettlementRuns.WithLabelValues(AuroraSettlementResultFailed)); got != 2 {
		t.Fatalf("settlement failed = %v, want 2", got)
	}
	if got := testutil.CollectAndCount(m.auroraSettlementRuns); got != len(AllAuroraSettlementResults()) {
		t.Fatalf("settlement series = %d, want %d", got, len(AllAuroraSettlementResults()))
	}
}

func TestAuroraBillingCreditShortfallCountsOne(t *testing.T) {
	m := NewBusinessMetrics()

	m.RecordAuroraCreditShortfall()

	if got := testutil.ToFloat64(m.auroraCreditShortfall); got != 1 {
		t.Fatalf("credit shortfalls = %v, want 1", got)
	}
}

// TestAuroraBillingMetricsNilReceiverIsSafe is the contract every handler call
// site relies on: Handler.Metrics may be nil (tests, older wiring), and a nil
// *BusinessMetrics must not panic on any of the Aurora billing recorders.
func TestAuroraBillingMetricsNilReceiverIsSafe(t *testing.T) {
	var m *BusinessMetrics

	m.RecordAuroraWebhookEvent("checkout.session.completed", AuroraWebhookResultOK)
	m.RecordAuroraSettlementRun(AuroraSettlementResultOK)
	m.RecordAuroraCreditShortfall()
}

func TestAuroraBillingMetricFamiliesAreExposed(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewBusinessMetrics()
	registry.MustRegister(m.Collectors()...)

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	seen := make(map[string]bool, len(families))
	for _, family := range families {
		seen[family.GetName()] = true
	}
	for _, name := range []string{
		"multica_aurora_billing_webhook_events_total",
		"multica_aurora_billing_settlement_runs_total",
		"multica_aurora_billing_credit_shortfalls_total",
	} {
		if !seen[name] {
			t.Fatalf("registry did not expose metric family %s", name)
		}
	}
}

// counterVecSum sums every series of a counter collector, which is how a test
// proves "exactly one series moved" on a vec that is prewarmed with zeros.
func counterVecSum(t *testing.T, c prometheus.Collector) float64 {
	t.Helper()
	registry := prometheus.NewRegistry()
	if err := registry.Register(c); err != nil {
		t.Fatalf("register collector: %v", err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var sum float64
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			if counter := metric.GetCounter(); counter != nil {
				sum += counter.GetValue()
			}
		}
	}
	return sum
}
