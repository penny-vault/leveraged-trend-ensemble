# Leveraged Trend Ensemble — Design Position Paper

**Status:** design draft, pre-implementation
**Date:** 2026-05-02
**Working name:** Leveraged Trend Ensemble (LTE) — placeholder, rename welcome

## Problem statement

Build a quantitative strategy that:

1. **Wins decisively in bull markets** by holding leveraged equity exposure when the regime supports it.
2. **Survives multiple bear regimes** without paying a structural drag (no permanent long-vol sleeves, no permanent insurance premiums).
3. **Captures the 2022 case specifically** — a rate-driven inflation bear where long Treasuries failed as a hedge alongside equity. Most leveraged-equity strategies (HEDGEFUNDIE 55/45 UPRO/TMF) lost 60%+ in 2022 because the bond hedge broke.
4. **Avoids the 2020 case as much as is possible without paying drag** — accepting that no signal-driven approach catches a 5-week 34% drop cleanly without long-vol exposure.
5. **Is built from positive-expected-return components.** Every asset, when held, has positive trailing momentum. No "insurance" sleeves that bleed in normal markets.

## Design philosophy and constraints

- Long-only (pvbt does not yet support short selling).
- No structural drag. Every component must be defensible as having positive standalone expectation when held.
- Lean toward simplicity, but accept layered structure where each layer addresses a distinct failure mode.
- Use research-backed mechanisms; avoid free parameters that aren't justified by published work or theory.
- Operate on broadly-available ETFs (TQQQ, QQQ, TLT, GLD, DBMF, BIL). No options, no custom derivatives, no proprietary data — except where pvbt's Zacks consensus estimate data adds optional alpha (deferred for now).

## Architecture overview

The strategy is built in four orthogonal layers, each addressing a distinct failure mode of trend-following:

| Layer | Mechanism | Failure mode it addresses |
|---|---|---|
| **Tranching** | Run the strategy on 4 weekly rebalance dates; portfolio = average of tranches | Single-day timing risk; concentrated rebalance whipsaw |
| **Vol-regime signal switching** | Each tranche selects its active trend signal (fast/medium/slow) based on current vol percentile | Slow signals miss high-vol regime changes; fast signals whipsaw in low-vol chop |
| **Tranche agreement → leverage selection** | Count of tranches in bullish state determines equity leverage (TQQQ vs QQQ vs cash) | Single-tranche timing error compounding via 3x leverage |
| **Vol kill switch** | Extreme realized vol (> 2× trailing baseline) triggers binary exit to cash regardless of trend signal | Fast V-shaped crashes (March 2020) that no trailing-momentum signal can catch |

The hedge basket (deployed when equity is exited) is a separate trend-following rotation across {TLT, GLD, DBMF, BIL} with TSMOM filtering. The hedge is itself responsible for surviving its own regimes.

## Component details

### Universe

**Equity sleeve (active when bullish):**
- TQQQ (3x leveraged Nasdaq-100) — held when conviction is high
- QQQ (1x Nasdaq-100) — held when conviction is moderate
- Cash (BIL) — held when conviction is mixed

**Hedge sleeve (active when equity exits):**
- TLT — long Treasuries, captures deflationary/recession bears (2008, 2020)
- GLD — gold, captures inflation bears and crisis flights (2022, 2020)
- DBMF — managed futures replication, captures trend-driven bears (2022, 2008, 2000-02)
- BIL — cash; floor allocation; absorbs slots when nothing else qualifies

**Why these specific assets:** each has positive trailing return in expectation, each captures a different bear regime. No long-vol sleeve; no permanent insurance.

### Tranching

Four weekly tranches, rebalancing on weeks 1, 2, 3, 4 of each month. Each tranche independently:

1. Computes current vol percentile (20-day realized vol of QQQ, expressed as a percentile of trailing 252-day distribution).
2. Selects the active trend signal based on vol regime (see next section).
3. Evaluates the active signal — bullish or bearish.
4. Returns its current state to the portfolio aggregator.

The portfolio is the average of the four tranches' positions. Tranching does two jobs simultaneously: (a) timing diversification — no single rebalance date dominates; (b) emergent smoothing — signal/regime transitions phase in across multiple weeks rather than as a single-day flip.

### Vol-regime signal switching

Three trend signals at different speeds:

- **Slow:** 12-month TSMOM (or Faber's 10-month SMA)
- **Medium:** 6-month TSMOM
- **Fast:** 3-month TSMOM

Vol regime determines which signal is active for a given tranche on a given rebalance date:

```
vol percentile < 0.33   → slow signal active
0.33 ≤ percentile < 0.67 → medium signal active
percentile ≥ 0.67       → fast signal active
```

The vol percentile is computed as the rank of current 20-day realized vol within the trailing 252-day distribution.

Hysteresis: once a regime is established, it persists for at least 10 trading days before reclassifying — even if vol percentile crosses the threshold. Prevents flip-flopping at boundaries.

**Why switching rather than weighted blending:** The Hurst/Hoffstein finding is that fast signals *outperform* slow signals in high-vol regimes — that's a statement about ordering, not magnitudes. Weighted blending requires arbitrary weights (60/30/10? 70/20/10?). Switching honors the research finding directly. The transition smoothness comes from tranching, not from explicit weight matrices.

### Tranche agreement → leverage selection

Each tranche votes bullish or bearish on equity. Count the tranches in agreement:

| Bullish tranches | Equity position |
|---|---|
| 4 of 4 | 100% TQQQ (3x conviction) |
| 3 of 4 | 100% QQQ (moderate conviction) |
| 2 of 4 | 100% BIL (no conviction; equity sidelined) |
| 0–1 of 4 | 100% hedge basket |

The 2/4 case sidelines equity but does not yet activate the hedge basket — interpretation: signals are mixed, hold cash and wait for clarity. Activating the hedge requires majority bearish (≥ 3 of 4 tranches).

This mapping is consistent with TQQQ's path-dependency cost: leveraged ETFs punish indecisive holdings. Only deploy 3x when all evidence agrees.

### Hedge basket

Activated when ≥ 3 of 4 tranches are bearish on equity. Constructed as:

1. Universe: {TLT, GLD, DBMF, BIL}.
2. Per-asset filter: include if trailing 6-month total return > 0.
3. Weighting: equal-weight among qualifiers.
4. BIL absorbs any unallocated weight — if all three risk-on hedges fail their TSMOM filter, the portfolio is 100% cash.
5. Rebalance monthly (not per-tranche; the hedge basket is a single allocation, not 4 sliced versions).

This handles the 2022 case directly: in early 2022 the trailing 6-month return on TLT went negative; TSMOM filter drops TLT; portfolio concentrates in GLD/DBMF/BIL, exactly the assets that worked in 2022.

### Vol kill switch

Independent of the trend ensemble. Computes:

```
vol_ratio = (20-day realized vol of QQQ) / (trailing 252-day median realized vol)
```

If `vol_ratio > 2.0` for two consecutive days, the equity sleeve is force-exited to BIL regardless of trend signal output. Re-entry only when:
- `vol_ratio < 1.5` for at least 10 consecutive trading days, AND
- The active trend signal (per tranche) is bullish at the next rebalance.

This is not a long-vol position. The portfolio holds no VIXY, no VIX futures, no puts. The vol ratio is read as a state indicator. When fired, the kill switch flips equity → cash; that's a position change, not a position.

False alarm cost is the limiting factor. Historical kill-switch firings:
- Feb 2018 (Volmageddon): vol spiked, recovered in 2 weeks. Kill switch missed ~1-2% of recovery.
- March 2020 (COVID): vol exploded; kill switch fires; portfolio avoids the worst of the drop and re-enters mid-recovery.
- Sep-Oct 2022: vol elevated but not extreme; kill switch may or may not fire depending on exact baseline.
- Aug 2024: brief vol spike from Yen carry-trade unwind; kill switch fires; market recovers in days.

Expected frequency: 2–4 firings per decade. Most fire correctly (precede sustained drawdowns); 1–2 are false alarms. Cost of false alarms is asymmetric with benefit — one true positive in a crash saves more than several false positives cost.

## Behavior across historical bear regimes

| Regime | Trend signals | Vol kill switch | Outcome |
|---|---|---|---|
| **2000–2002 (slow grind, deflationary)** | Slow signal active most of the time (low-vol regime); equity exits early as 12-month return turns negative | Doesn't fire (vol elevated but not extreme) | Equity exits via trend; hedge basket holds TLT (positive momentum throughout) and gold (late). Portfolio compounds modestly through a 49% SPY drawdown. |
| **2008 (deflationary crash)** | Slow/medium signals turn bearish in October 2008; equity exits | Fires September–October as vol spikes | Equity exits; hedge basket dominated by TLT (massive positive return) and BIL. Captures flight to quality. |
| **2020 (V-shaped COVID)** | Trend signals don't flip in time; would have eaten 25-30% of TQQQ drop without intervention | Fires mid-March; equity force-exits to cash | Eats ~10-15% of drop before kill switch; misses lower portion; re-enters mid-recovery. Worst-case regime for the architecture; partial protection. |
| **2022 (rate-driven inflation)** | Medium/fast signals turn bearish on TQQQ early; signals on TLT also bearish (rate environment); equity exits to hedge basket; hedge basket TSMOM filter drops TLT (negative momentum) | May fire briefly mid-year | Equity exits to hedge basket; hedge basket holds GLD + DBMF + BIL (all positive trailing return); portfolio finishes positive while SPY -18%, HEDGEFUNDIE -60%+. The case the architecture explicitly solves. |

## Research foundations

- **Hurst, Ooi, Pedersen "A Century of Evidence on Trend-Following Investing" (AQR, 2017).** 137-year backtest showing time-series momentum produced positive returns in every major equity drawdown since 1880. Foundation for the trend filter.
- **Asness, Moskowitz, Pedersen "Time Series Momentum" (Journal of Financial Economics, 2012).** Establishes 12-month TSMOM as the canonical signal across asset classes.
- **Hurst, Ooi, Pedersen "Demystifying Managed Futures" (2017).** Empirical finding that fast trend signals outperform slow signals in high-vol regimes; basis for the regime-switching layer.
- **Hoffstein/Newfound Research, multiple papers (2017–2022).** "What if 8 of the smartest trend-followers can't agree?", "Tactical Asset Allocation Through Risk Cycles", "Diversifying the What, How, and When". Establishes ensembling as variance-reduction technique and tranching as orthogonal smoothing.
- **Antonacci "Dual Momentum Investing" (2014).** Combines absolute (TSMOM) and relative momentum; influences the rotation logic in the hedge basket.
- **Faber "A Quantitative Approach to Tactical Asset Allocation" (2007).** Monthly close vs 10-month SMA as the canonical slow trend signal; influences signal selection.
- **Cole / Artemis Capital "The Allegory of the Hawk and Serpent" (2020).** Multi-regime portfolio construction; informed the explicit handling of 2022-style inflation bears.

What is *not* directly drawn on: pure risk parity (Bridgewater All Weather), Adaptive Asset Allocation (Butler/Philbrick/Gordillo). Both require optimizer infrastructure that pvbt does not yet have. Their core ideas — diversification across asset classes, regime-adaptive weighting — are expressed in this design through simpler mechanisms (TSMOM filter + tranching + vol-regime switching).

## Approaches considered and rejected

- **Long-vol sleeves (Dragon Portfolio, Universa-style tail hedging, TAIL ETF, VIXY allocation).** Rejected because they pay structural drag (~5%/year) for tail protection. Goal was positive-expectation components only.
- **HEDGEFUNDIE 55/45 UPRO/TMF risk parity.** Rejected because TMF lost 71% in 2022; the bond hedge is not regime-robust. Captured by the broader concern about static fixed allocations.
- **Fixed All-Weather allocation.** Rejected for the same reason; failed in 2022 because long bonds dropped alongside equity. Static diversification doesn't help when correlations spike during regime change.
- **Single-signal trend (basic Faber, basic 12mo TSMOM on a basket).** Considered; rejected as insufficiently novel and insufficiently robust to single-signal failure modes.
- **Performance-weighted ensembles (weight signals by recent returns).** Rejected on academic grounds: DeMiguel/Garlappi/Uppal (2009) and the broader performance-chasing literature show this destroys value out-of-sample.
- **Cross-sectional growth selection using Zacks consensus data.** Deferred but not abandoned. Could be added as the bull-leg replacement (instead of TQQQ/QQQ) once the macro framework here is validated. The estimate data covers 2021–present, which is enough to backtest as an addition but would require pvbt to expose the metrics.
- **Combined cross-sectional + macro design in a single strategy.** Deferred; agreed to focus on the macro layer first and consider stock-picking as an enhancement later.
- **Adaptive Asset Allocation (Butler/Philbrick/Gordillo).** Deferred until pvbt has minimum-variance optimizer infrastructure. Once that exists, AAA becomes one strategy of many that benefit from the optimizer.

## Open design questions

1. **Vol percentile basis.** Trailing 252 days adapts to recent regime but means "high vol" in 2017 means something different than in 2020. Alternatives: trailing 5 years (more stable but less adaptive), full-sample (most stable but loses regime adaptiveness). Tentative choice: trailing 252.
2. **Hysteresis duration.** 10 trading days is a guess. Could be 5, 15, 20. More hysteresis = fewer false regime flips but slower legitimate transitions. Backtesting can refine.
3. **Vol indicator.** Realized 20-day is the cleanest backtestable choice. VIX adds forward-looking information but introduces futures-curve dynamics. A blended indicator (max of realized and implied) could be considered. Tentative choice: realized 20-day.
4. **Whether to add UPRO and SOXL to the equity sleeve.** Currently: TQQQ only, on the assumption that QQQ-leveraged exposure captures the strategy's intent. Alternatives: rotate among {TQQQ, UPRO, SOXL} based on relative strength, capturing whichever leveraged equity is leading. Adds complexity; benefit unclear.
5. **TSMOM lookback in the hedge basket.** Currently 6 months. Could be 3 or 12. 12 is more conservative (slower to drop hedges); 3 is more reactive (faster to drop broken hedges). 6 is the middle ground.
6. **Whether to vol-target the hedge basket.** Currently equal-weight; could weight by inverse trailing vol so each qualifier contributes equal portfolio risk. Adds complexity; modest benefit.
7. **Re-entry logic after vol kill switch.** Currently requires (vol_ratio < 1.5) for 10 days AND active trend signal bullish. The 10-day requirement is conservative. Could be 5 days. Tradeoff: faster re-entry catches more recovery but risks re-entering before vol fully resolves.
8. **The "regular bull" QQQ allocation when 3 of 4 tranches agree.** Should this position be 100% QQQ, or something like 50% QQQ + 50% BIL (smoother transition into TQQQ when fourth tranche flips)? Probably 100% QQQ is correct — tranching itself provides the smoothing.
9. **Backtest data availability pre-2010.** Most ETFs in the universe don't have ETF history before 2010. Backtesting 2000–2010 requires either underlying-asset proxies (long-Treasury index, gold spot, CTA index) or accepting that pre-2010 backtests are synthetic. Need to scope what pvbt has.

## Implementation scope

The strategy as described requires no new pvbt features. All components use existing data (daily OHLCV, ETF universes), existing engine APIs (FetchAt, IndexUniverse, schedule machinery), and existing Go primitives.

The build sequence:

1. **Phase 1: monolithic strategy in pvbt's strategy package layout.** Single Go module with the full architecture (tranching, vol regime switching, leverage selection, hedge basket, kill switch). Test against 2010–2024 backtest. Estimated 1–2 days of focused work.
2. **Phase 2: parameter exploration.** Vary hysteresis, vol thresholds, hedge-basket TSMOM lookback. Identify which parameters are robust vs which are fragile. Don't optimize — characterize the parameter sensitivity.
3. **Phase 3: presets.** Ship 2–3 named configurations:
   - `default`: as described above.
   - `aggressive`: faster regime detection, lower vol kill threshold, more time in TQQQ.
   - `conservative`: longer hysteresis, higher vol kill threshold, more time in QQQ rather than TQQQ.
4. **Phase 4 (optional, future):** add cross-sectional bull leg using Zacks estimate data once pvbt exposes those metrics.

## Things this strategy is NOT

- It is not a buy-and-hold replacement. Drawdowns will still happen; 2020-style V-shapes will still hurt.
- It is not a low-vol product. Volatility will be high — TQQQ exposure during bulls is the source of returns.
- It is not regime-agnostic. It explicitly bets that bears will be slow enough to detect via 3-month or longer trend signals; V-shaped crashes will damage the portfolio before the kill switch fires.
- It is not parameter-free. The vol thresholds, hysteresis duration, TSMOM lookbacks, and tranche count are all design choices that have meaningful effect on outcomes. Backtesting will refine them but cannot eliminate the parameter dependence.
- It is not novel in its individual layers. Each layer (tranching, ensemble trend, vol-regime switching, multi-asset hedge) appears separately in published research. The novelty is the specific combination and the explicit handling of the 2022 case via the hedge basket's own TSMOM filter.

## Summary

A leveraged-equity-with-dynamic-hedge strategy built from four orthogonal layers, each addressing a distinct failure mode. Targets the 2022 case specifically (where most leveraged strategies failed) by routing the hedge through its own TSMOM filter rather than a fixed bond allocation. Accepts that 2020-style V-shapes are partially but not fully addressable without paying drag. Uses TQQQ for high-conviction bulls, QQQ for moderate bulls, and rotates to a TSMOM-filtered hedge basket of {TLT, GLD, DBMF, BIL} during bear regimes. All components have positive standalone expected return when held; no insurance sleeves, no structural drag.
