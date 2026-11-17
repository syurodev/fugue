package app

// Package app owns the Fugue tool surface for team proposals (F11.2a). This
// file holds the usage-history port and the §5.3 estimate rendering.

import (
	"context"
	"fmt"
	"slices"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Units a usage history may report (§5.3): percent of the vendor's quota
// window for subscription accounts, or USD for metered accounts. The two are
// never mixed in one estimate.
const (
	usageUnitPercent = "percent_quota"
	usageUnitUSD     = "usd"
)

// usageHistory is the port the proposal card's usage estimate reads. A source
// returns the recorded per-task usage of one role and model, oldest last, in
// one unit; ok is false when no usable history exists. Stage A has none — no
// team task has ever run — so the wired source is nopUsageHistory until F11.3
// starts tasks and F9.10 records quota signals.
type usageHistory interface {
	RecentUsage(ctx context.Context, role, model string) (values []float64, unit string, ok bool, err error)
}

// nopUsageHistory answers without history, so every estimate says "no data
// yet" instead of guessing (§5.3).
type nopUsageHistory struct{}

// RecentUsage implements usageHistory with no data.
func (nopUsageHistory) RecentUsage(context.Context, string, string) ([]float64, string, bool, error) {
	return nil, "", false, nil
}

// medianUsage renders the usage estimate of one role: the median of the last
// 20 recorded tasks, or ok=false when there is nothing to estimate. A source
// that returns values without a known unit yields no estimate either — the
// card never guesses.
func medianUsage(values []float64, unit string) (string, bool) {
	if len(values) == 0 || unit != usageUnitPercent && unit != usageUnitUSD {
		return "", false
	}
	if len(values) > usageSampleSize {
		values = values[len(values)-usageSampleSize:]
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	median := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		median = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
	}
	if unit == usageUnitPercent {
		return fmt.Sprintf("%.4g%% of the quota window per task", median), true
	}
	return fmt.Sprintf("$%.4g per task", median), true
}

// usageSampleSize is the §5.3 window: the median of the last 20 tasks.
const usageSampleSize = 20

// usageEstimateLine renders one role's card line. Errors of the history
// source fail the proposal: a card with a silent gap would look like data.
func usageEstimateLine(ctx context.Context, h usageHistory, name, vendor, model string) (string, error) {
	values, unit, ok, err := h.RecentUsage(ctx, name, model)
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "read the role usage history", err)
	}
	if !ok {
		return "- " + name + " (" + vendor + " " + model + "): no usage data yet", nil
	}
	estimate, known := medianUsage(values, unit)
	if !known {
		return "- " + name + " (" + vendor + " " + model + "): no usage data yet", nil
	}
	return "- " + name + " (" + vendor + " " + model + "): about " + estimate, nil
}
