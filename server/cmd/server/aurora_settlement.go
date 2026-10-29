package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/aurora"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// auroraSettlementInterval is how often the monthly settlement re-runs. A day
// is far finer than the month it settles: the loop is not what defines the
// window (RunMonthlySettlement derives that from the clock), so running it
// often only means a missed 1st is noticed sooner.
const auroraSettlementInterval = 24 * time.Hour

// startAuroraSettlement runs Aurora's monthly credit settlement. It runs once
// at startup and then daily; every run is idempotent via month-scoped ledger
// references, so a missed tick or a restart across the 1st self-heals on the
// next run. Failures are logged and retried on the next tick rather than
// stopping the loop — a settlement that stops silently would leave paying
// users ungranted, which is worse than a loud, repeated failure. Each run is
// counted by outcome so that failure can be alerted on before the next tick.
// A run aborted by a cancelled context is the graceful-shutdown path, not a
// failed settlement: recording it as failed would page on every restart until
// the next run, so it is logged but not counted.
func startAuroraSettlement(ctx context.Context, queries *db.Queries, credit *aurora.CreditService, tiers *aurora.TierCatalog, metrics *obsmetrics.BusinessMetrics) {
	run := func() {
		if err := aurora.RunMonthlySettlement(ctx, queries, credit, tiers, time.Now()); err != nil {
			// A cancelled context means shutdown interrupted the run; it is not
			// a settlement failure and must not be alerted on as one.
			if errors.Is(err, context.Canceled) {
				slog.Info("aurora monthly settlement cancelled", "error", err)
				return
			}
			metrics.RecordAuroraSettlementRun(obsmetrics.AuroraSettlementResultFailed)
			slog.Error("aurora monthly settlement failed", "error", err)
			return
		}
		metrics.RecordAuroraSettlementRun(obsmetrics.AuroraSettlementResultOK)
	}
	run()
	ticker := time.NewTicker(auroraSettlementInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
