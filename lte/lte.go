// Copyright 2026
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lte

import (
	"context"
	_ "embed"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/penny-vault/pvbt/asset"
	"github.com/penny-vault/pvbt/data"
	"github.com/penny-vault/pvbt/engine"
	"github.com/penny-vault/pvbt/portfolio"
	"github.com/penny-vault/pvbt/universe"
	"github.com/rs/zerolog"
)

//go:embed README.md
var description string

// Hard-coded design constants. These are not exposed as parameters because
// they are baked into the architecture of the strategy; changing them would
// produce a different strategy, not a tuning of this one.
const (
	// Realized-vol window for the regime classifier and kill switch.
	volWindowDays = 20

	// Trailing distribution window used for both the regime percentile and
	// the kill-switch median baseline.
	volBaselineDays = 252

	// Vol-percentile breakpoints between slow / medium / fast regimes.
	regimeLowBreak  = 0.33
	regimeHighBreak = 0.67

	// Trend-signal lookbacks (trading days).
	slowLookbackDays   = 252 // ~12 months
	mediumLookbackDays = 126 // ~6 months
	fastLookbackDays   = 63  // ~3 months

	// Hedge-basket TSMOM lookback (trading days).
	hedgeLookbackDays = 126 // ~6 months

	// Re-entry: vol_ratio must be below this for `reentryCoolDays` consecutive days.
	reentryRatio    = 1.5
	reentryCoolDays = 10

	// Number of tranches in the ensemble.
	trancheCount = 4
)

// LeveragedTrendEnsemble is a four-layer leveraged trend strategy: tranching
// + vol-regime signal switching + leverage selection + vol kill switch, with
// a TSMOM-filtered hedge basket as the bear-regime sleeve.
type LeveragedTrendEnsemble struct {
	Levered universe.Universe `pvbt:"levered" desc:"Leveraged equity ETF held when 4/4 tranches are bullish (single ticker; defaults to a TQQQ-with-QLD-fallback splice in Setup)"`
	Regular universe.Universe `pvbt:"regular" desc:"1x equity ETF held when 3/4 tranches are bullish; also the trend/vol leader" default:"QQQ"`
	Cash    universe.Universe `pvbt:"cash-asset" desc:"Cash equivalent held when 2/4 tranches are bullish or kill switch fires (single ticker; defaults to a BIL-with-SHY-fallback splice in Setup)"`
	Hedges  universe.Universe `pvbt:"hedges"  desc:"Risk-on hedges activated when 0-1/4 tranches are bullish; failed slots collapse to the spliced Cash universe" default:"TLT,GLD"`

	VolKillThreshold float64 `pvbt:"vol-kill-threshold" desc:"Vol ratio (current 20d / trailing 252d median) that triggers the kill switch when sustained for two consecutive days" default:"2.0" suggest:"Default=2.0|Aggressive=2.5|Conservative=1.75"`
}

func (s *LeveragedTrendEnsemble) Name() string {
	return "Leveraged Trend Ensemble"
}

func (s *LeveragedTrendEnsemble) Setup(eng *engine.Engine) {
	// Splice the leveraged sleeve: TQQQ from its inception, QLD before that.
	// QLD provides 2x exposure where TQQQ provides 3x; pre-2010 returns are
	// understated as a result, but the splice unlocks pre-2010 backtesting.
	if s.Levered == nil {
		s.Levered = eng.SpliceUniverse("TQQQ", universe.SplicePeriod{
			Ticker: "QLD",
			Before: time.Date(2010, 2, 11, 0, 0, 0, 0, time.UTC),
		})
	}
	// Splice the cash sleeve: BIL from its inception, SHY before that. SHY
	// has duration risk that BIL does not, but the duration is short enough
	// that pre-2007 cash behavior is approximately correct.
	if s.Cash == nil {
		s.Cash = eng.SpliceUniverse("BIL", universe.SplicePeriod{
			Ticker: "SHY",
			Before: time.Date(2007, 5, 25, 0, 0, 0, 0, time.UTC),
		})
	}
}

func (s *LeveragedTrendEnsemble) Describe() engine.StrategyDescription {
	return engine.StrategyDescription{
		ShortCode:   "lte",
		Description: description,
		Source:      "https://github.com/penny-vault/strategies/tree/main/leveraged-trend-ensemble",
		Version:     "0.1.0",
		VersionDate: time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC),
		Schedule:    "@weekend",
		Benchmark:   "SPY",
		// Slow trend (252) + the oldest tranche's rebalance offset (~21 trading
		// days back) needs ~273 days of history to evaluate at that tranche's
		// most recent rebalance. The vol percentile chain (1 + 19 + 251 = 271)
		// is in the same neighborhood. Round up for safety.
		Warmup: 320,
	}
}

func (s *LeveragedTrendEnsemble) Compute(ctx context.Context, eng *engine.Engine, port portfolio.Portfolio, batch *portfolio.Batch) error {
	log := zerolog.Ctx(ctx)
	today := eng.CurrentDate()

	// Pull enough leader history to cover the slow-trend lookback applied at
	// the oldest tranche's rebalance date, plus the vol-percentile chain.
	// Months(15) ~ 315 trading days, comfortably above slowLookbackDays + 4-week
	// tranche offset and the vol-percentile chain (1 + 19 + 251 = 271 days).
	leaderDF, err := s.Regular.Window(ctx, portfolio.Months(15), data.AdjClose)
	if err != nil {
		log.Error().Err(err).Msg("failed to fetch leader prices")
		return fmt.Errorf("fetch leader prices: %w", err)
	}

	if leaderDF.Len() < slowLookbackDays+1 {
		log.Warn().Int("len", leaderDF.Len()).Msg("insufficient leader history; skipping tick")
		return nil
	}

	leaderAssets := leaderDF.AssetList()
	if len(leaderAssets) == 0 {
		return fmt.Errorf("leader universe empty")
	}
	leader := leaderAssets[0]

	prices := leaderDF.Column(leader, data.AdjClose)
	times := leaderDF.Times()

	// Daily returns -> 20-day rolling stdev of returns -> the inputs the
	// vol-percentile and vol-ratio computations both share.
	dailyReturns := pctChange1(prices)
	realizedVol := rollingStd(dailyReturns, volWindowDays)

	// Vol percentile: rolling rank of today's realized vol within the
	// trailing 252-day distribution. Used by the regime classifier.
	volPct := rollingPercentileRank(realizedVol, volBaselineDays)

	// Vol ratio: realized vol divided by the 252-day rolling median. Used
	// by the kill switch.
	volMedian := rollingMedian(realizedVol, volBaselineDays)
	volRatio := elementwiseDiv(realizedVol, volMedian)

	killOn := killSwitchActive(volRatio, s.VolKillThreshold)
	batch.Annotate("kill_switch", strconv.FormatBool(killOn))

	if v := lastFinite(realizedVol); !math.IsNaN(v) {
		batch.Annotate("realized_vol_20d", strconv.FormatFloat(v, 'f', -1, 64))
	}
	if v := lastFinite(volRatio); !math.IsNaN(v) {
		batch.Annotate("vol_ratio", strconv.FormatFloat(v, 'f', -1, 64))
	}
	if v := lastFinite(volPct); !math.IsNaN(v) {
		batch.Annotate("vol_percentile", strconv.FormatFloat(v, 'f', -1, 64))
	}

	// Re-evaluate each tranche at its own most-recent rebalance date.
	bullCount := 0
	for trancheIdx := 0; trancheIdx < trancheCount; trancheIdx++ {
		rebalanceDate := mostRecentTrancheDate(today, trancheIdx)
		idx := lastIdxOnOrBefore(times, rebalanceDate)
		if idx < slowLookbackDays {
			// Not enough history to evaluate this tranche's slow signal yet.
			continue
		}

		bullish := evaluateTranche(prices, volPct, idx)
		if bullish {
			bullCount++
		}
		batch.Annotate(fmt.Sprintf("tranche_%d_bullish", trancheIdx), strconv.FormatBool(bullish))
	}
	batch.Annotate("bull_count", strconv.Itoa(bullCount))

	leveredAsset, err := singleAsset(s.Levered, today)
	if err != nil {
		return fmt.Errorf("resolve levered: %w", err)
	}
	regularAsset, err := singleAsset(s.Regular, today)
	if err != nil {
		return fmt.Errorf("resolve regular: %w", err)
	}
	cashAsset, err := singleAsset(s.Cash, today)
	if err != nil {
		return fmt.Errorf("resolve cash: %w", err)
	}

	currentSleeve := s.classifySleeve(port, today, leveredAsset, regularAsset, cashAsset)
	batch.Annotate("current_sleeve", currentSleeve)

	currentVolPct := lastFinite(volPct)
	volRegime := classifyVolRegime(currentVolPct)
	batch.Annotate("vol_regime", volRegime)

	members, justification, err := s.equitySleeve(ctx, bullCount, killOn, currentSleeve, volRegime, leveredAsset, regularAsset, cashAsset)
	if err != nil {
		log.Error().Err(err).Msg("failed to build sleeve allocation")
		return fmt.Errorf("build sleeve allocation: %w", err)
	}

	batch.Annotate("justification", justification)

	allocation := portfolio.Allocation{
		Date:          today,
		Members:       members,
		Justification: justification,
	}

	if err := batch.RebalanceTo(ctx, allocation); err != nil {
		log.Error().Err(err).Msg("failed to rebalance")
		return fmt.Errorf("rebalance: %w", err)
	}

	return nil
}

// Sleeve identifiers used by the hysteresis state machine.
const (
	sleeveLevered = "levered"
	sleeveRegular = "regular"
	sleeveCash    = "cash"
	sleeveHedge   = "hedge"
)

// equitySleeve maps the tranche-bullish count, kill-switch state, and
// CURRENT sleeve to a target allocation. The state machine has asymmetric
// hysteresis: once in a higher-leverage sleeve, the bullCount must drop
// further than the entry threshold before downgrading. This stops chop years
// from round-tripping TQQQ at every 4->3 oscillation.
//
// Transitions (target as a function of current state and bullCount):
//
//	current=hedge:    bull>=2 -> cash, bull>=3 -> regular, bull==4 -> levered
//	current=cash:     bull==4 -> levered, bull==3 -> regular, bull==0 -> hedge
//	current=regular:  bull==4 -> levered, bull<=1 -> cash, bull==0 -> hedge
//	current=levered:  bull<=2 -> regular, bull<=1 -> cash, bull==0 -> hedge
func (s *LeveragedTrendEnsemble) equitySleeve(ctx context.Context, bullCount int, killOn bool, current string, volRegime string, levered, regular, cash asset.Asset) (map[asset.Asset]float64, string, error) {
	if killOn {
		return map[asset.Asset]float64{cash: 1.0}, "kill switch active: 100% cash", nil
	}

	target := capByVolRegime(nextSleeve(current, bullCount), volRegime)

	switch target {
	case sleeveLevered:
		return map[asset.Asset]float64{levered: 1.0}, fmt.Sprintf("%d/4 bullish (from %s): 100%% %s", bullCount, current, levered.Ticker), nil
	case sleeveRegular:
		return map[asset.Asset]float64{regular: 1.0}, fmt.Sprintf("%d/4 bullish (from %s): 100%% %s", bullCount, current, regular.Ticker), nil
	case sleeveCash:
		return map[asset.Asset]float64{cash: 1.0}, fmt.Sprintf("%d/4 bullish (from %s): 100%% cash", bullCount, current), nil
	default: // sleeveHedge
		members, err := s.computeHedgeBasket(ctx, cash)
		if err != nil {
			return nil, "", err
		}
		return members, fmt.Sprintf("%d/4 bullish (from %s): hedge basket", bullCount, current), nil
	}
}

func nextSleeve(current string, bullCount int) string {
	switch current {
	case sleeveLevered:
		switch {
		case bullCount >= 3:
			return sleeveLevered
		case bullCount == 2:
			return sleeveRegular
		case bullCount == 1:
			return sleeveCash
		default:
			return sleeveHedge
		}
	case sleeveRegular:
		switch {
		case bullCount == 4:
			return sleeveLevered
		case bullCount >= 2:
			return sleeveRegular
		case bullCount == 1:
			return sleeveCash
		default:
			return sleeveHedge
		}
	case sleeveCash:
		switch {
		case bullCount == 4:
			return sleeveLevered
		case bullCount == 3:
			return sleeveRegular
		case bullCount >= 1:
			return sleeveCash
		default:
			return sleeveHedge
		}
	case sleeveHedge:
		switch {
		case bullCount == 4:
			return sleeveLevered
		case bullCount == 3:
			return sleeveRegular
		case bullCount >= 2:
			return sleeveCash
		default:
			return sleeveHedge
		}
	default:
		// No prior state (first tick / unrecognized): apply the strict
		// thresholds. Subsequent ticks pick up the hysteresis.
		switch bullCount {
		case 4:
			return sleeveLevered
		case 3:
			return sleeveRegular
		case 2:
			return sleeveCash
		default:
			return sleeveHedge
		}
	}
}

// Vol-regime labels emitted by classifyVolRegime.
const (
	volRegimeLow    = "low"
	volRegimeMedium = "medium"
	volRegimeHigh   = "high"
)

// classifyVolRegime maps a vol percentile to a regime label using the same
// breakpoints as the trend-signal selector. NaN -> low (most permissive,
// applies before warmup completes).
func classifyVolRegime(volPercentile float64) string {
	switch {
	case math.IsNaN(volPercentile):
		return volRegimeLow
	case volPercentile < regimeLowBreak:
		return volRegimeLow
	case volPercentile < regimeHighBreak:
		return volRegimeMedium
	default:
		return volRegimeHigh
	}
}

// capByVolRegime caps the target sleeve based on current realized-vol regime.
// In high-vol regimes the leveraged sleeve is replaced by 1x equity; the 1x
// equity sleeve is preserved (we still want trend-following exposure). Low
// and medium vol regimes leave the sleeve unchanged. The hedge sleeve is
// unaffected -- it has its own internal TSMOM filter.
func capByVolRegime(target string, volRegime string) string {
	if target == sleeveLevered && volRegime == volRegimeHigh {
		return sleeveRegular
	}
	return target
}

// classifySleeve infers the current equity sleeve from the portfolio's
// holdings. Used to drive the hysteresis state machine. The cash ticker is
// the cash sleeve unless any non-cash hedge is also held, in which case the
// position is the hedge sleeve.
func (s *LeveragedTrendEnsemble) classifySleeve(port portfolio.Portfolio, today time.Time, levered, regular, cash asset.Asset) string {
	if port.Position(levered) > 0 {
		return sleeveLevered
	}
	if port.Position(regular) > 0 {
		return sleeveRegular
	}
	for _, a := range s.Hedges.Assets(today) {
		if a == cash {
			continue
		}
		if port.Position(a) > 0 {
			return sleeveHedge
		}
	}
	if port.Position(cash) > 0 {
		return sleeveCash
	}
	return ""
}

// singleAsset returns the single member of a single-asset universe at the
// given date. Returns an error if the universe is empty.
func singleAsset(u universe.Universe, today time.Time) (asset.Asset, error) {
	members := u.Assets(today)
	if len(members) == 0 {
		return asset.Asset{}, fmt.Errorf("universe is empty at %s", today.Format("2006-01-02"))
	}
	return members[0], nil
}

// computeHedgeBasket builds the bear-regime allocation. Each non-cash hedge
// slot fills if its trailing 6-month return > 0; failed slots collapse to
// the cash ticker.
func (s *LeveragedTrendEnsemble) computeHedgeBasket(ctx context.Context, cash asset.Asset) (map[asset.Asset]float64, error) {
	df, err := s.Hedges.Window(ctx, portfolio.Months(8), data.AdjClose)
	if err != nil {
		return nil, fmt.Errorf("fetch hedge prices: %w", err)
	}
	if df.Len() < hedgeLookbackDays+1 {
		return nil, fmt.Errorf("insufficient hedge history: need %d trading days, got %d", hedgeLookbackDays+1, df.Len())
	}

	returns := df.Pct(hedgeLookbackDays).Last()
	if err := returns.Err(); err != nil {
		return nil, fmt.Errorf("compute hedge returns: %w", err)
	}

	riskOn := []asset.Asset{}
	for _, a := range returns.AssetList() {
		if a == cash {
			continue
		}
		riskOn = append(riskOn, a)
	}

	members := make(map[asset.Asset]float64)
	if len(riskOn) == 0 {
		members[cash] = 1.0
		return members, nil
	}

	slotWeight := 1.0 / float64(len(riskOn))
	cashWeight := 0.0
	for _, a := range riskOn {
		v := returns.Value(a, data.AdjClose)
		if !math.IsNaN(v) && v > 0 {
			members[a] += slotWeight
		} else {
			cashWeight += slotWeight
		}
	}
	if cashWeight > 0 {
		members[cash] += cashWeight
	}
	return members, nil
}

// evaluateTranche returns true if the active trend signal at index `idx`
// (selected by the vol percentile at that index) is positive.
func evaluateTranche(prices []float64, volPct []float64, idx int) bool {
	pct := volPct[idx]
	if math.IsNaN(pct) {
		// Fall back to the slow signal when the vol regime is undefined --
		// it's the most conservative default.
		pct = 0.0
	}

	var lookback int
	switch {
	case pct < regimeLowBreak:
		lookback = slowLookbackDays
	case pct < regimeHighBreak:
		lookback = mediumLookbackDays
	default:
		lookback = fastLookbackDays
	}

	if idx < lookback {
		return false
	}

	past := prices[idx-lookback]
	if math.IsNaN(past) || past <= 0 {
		return false
	}
	now := prices[idx]
	if math.IsNaN(now) {
		return false
	}
	return now > past
}

// killSwitchActive walks the vol_ratio history forward and returns whether
// the kill switch would currently be on. The state machine: ARMED until
// vol_ratio > threshold for two consecutive days (-> ON); ON until
// vol_ratio < reentryRatio for `reentryCoolDays` consecutive days (-> ARMED).
func killSwitchActive(volRatio []float64, threshold float64) bool {
	on := false
	coolDays := 0
	for i := 1; i < len(volRatio); i++ {
		curr := volRatio[i]
		prev := volRatio[i-1]
		if math.IsNaN(curr) || math.IsNaN(prev) {
			continue
		}
		if !on {
			if curr > threshold && prev > threshold {
				on = true
				coolDays = 0
			}
			continue
		}
		if curr < reentryRatio {
			coolDays++
			if coolDays >= reentryCoolDays {
				on = false
				coolDays = 0
			}
		} else {
			coolDays = 0
		}
	}
	return on
}

// mostRecentTrancheDate returns the most recent past date (within the last
// four weeks) whose week-of-cycle assigns it to `trancheIdx`. The week of
// cycle is the ISO year/week combined into a monotonic integer modulo 4.
func mostRecentTrancheDate(today time.Time, trancheIdx int) time.Time {
	d := today
	for i := 0; i < trancheCount; i++ {
		if weekOfCycle(d)%trancheCount == trancheIdx {
			return d
		}
		d = d.AddDate(0, 0, -7)
	}
	return d
}

func weekOfCycle(d time.Time) int {
	year, week := d.ISOWeek()
	// Each ISO year has 52 or 53 weeks; 53 is a stable upper bound.
	return year*53 + week
}

// lastIdxOnOrBefore returns the largest index i such that times[i] <= target.
// Returns -1 if no such index exists. Times are assumed strictly increasing.
func lastIdxOnOrBefore(times []time.Time, target time.Time) int {
	last := -1
	for i, t := range times {
		if t.After(target) {
			break
		}
		last = i
	}
	return last
}

// pctChange1 returns the one-period percent change of the input series. The
// first element is NaN.
func pctChange1(series []float64) []float64 {
	out := make([]float64, len(series))
	if len(series) == 0 {
		return out
	}
	out[0] = math.NaN()
	for i := 1; i < len(series); i++ {
		prev := series[i-1]
		if math.IsNaN(prev) || prev == 0 {
			out[i] = math.NaN()
			continue
		}
		out[i] = (series[i] - prev) / prev
	}
	return out
}

// rollingStd computes the rolling sample standard deviation (N-1 denom) over
// a window of `n` values. Positions before the window is full are NaN.
func rollingStd(series []float64, n int) []float64 {
	out := make([]float64, len(series))
	for i := range out {
		out[i] = math.NaN()
	}
	if n < 2 || len(series) < n {
		return out
	}
	for i := n - 1; i < len(series); i++ {
		valid := 0
		sum := 0.0
		for j := i - n + 1; j <= i; j++ {
			v := series[j]
			if math.IsNaN(v) {
				continue
			}
			valid++
			sum += v
		}
		if valid < 2 {
			continue
		}
		mean := sum / float64(valid)
		ss := 0.0
		for j := i - n + 1; j <= i; j++ {
			v := series[j]
			if math.IsNaN(v) {
				continue
			}
			d := v - mean
			ss += d * d
		}
		out[i] = math.Sqrt(ss / float64(valid-1))
	}
	return out
}

// rollingMedian computes the rolling median over a window of `n` values.
// Positions before the window is full are NaN.
func rollingMedian(series []float64, n int) []float64 {
	out := make([]float64, len(series))
	for i := range out {
		out[i] = math.NaN()
	}
	if n < 1 || len(series) < n {
		return out
	}
	buf := make([]float64, 0, n)
	for i := n - 1; i < len(series); i++ {
		buf = buf[:0]
		for j := i - n + 1; j <= i; j++ {
			v := series[j]
			if !math.IsNaN(v) {
				buf = append(buf, v)
			}
		}
		if len(buf) == 0 {
			continue
		}
		out[i] = median(buf)
	}
	return out
}

// rollingPercentileRank returns the percentile rank of series[i] within
// {series[i-n+1], ..., series[i]} (count of values <= series[i] divided by
// the count of valid observations in the window). Positions before the
// window is full or where series[i] is NaN are NaN.
func rollingPercentileRank(series []float64, n int) []float64 {
	out := make([]float64, len(series))
	for i := range out {
		out[i] = math.NaN()
	}
	if n < 1 || len(series) < n {
		return out
	}
	for i := n - 1; i < len(series); i++ {
		ref := series[i]
		if math.IsNaN(ref) {
			continue
		}
		valid := 0
		atOrBelow := 0
		for j := i - n + 1; j <= i; j++ {
			v := series[j]
			if math.IsNaN(v) {
				continue
			}
			valid++
			if v <= ref {
				atOrBelow++
			}
		}
		if valid > 0 {
			out[i] = float64(atOrBelow) / float64(valid)
		}
	}
	return out
}

func elementwiseDiv(num, den []float64) []float64 {
	n := len(num)
	if len(den) < n {
		n = len(den)
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		a := num[i]
		b := den[i]
		if math.IsNaN(a) || math.IsNaN(b) || b == 0 {
			out[i] = math.NaN()
			continue
		}
		out[i] = a / b
	}
	return out
}

func median(values []float64) float64 {
	n := len(values)
	if n == 0 {
		return math.NaN()
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return 0.5 * (sorted[n/2-1] + sorted[n/2])
}

func lastFinite(series []float64) float64 {
	for i := len(series) - 1; i >= 0; i-- {
		if !math.IsNaN(series[i]) {
			return series[i]
		}
	}
	return math.NaN()
}
