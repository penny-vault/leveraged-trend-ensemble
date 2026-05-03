# Leveraged Trend Ensemble

A leveraged-equity-with-dynamic-hedge strategy targeting the 2022 inflation bear case. Built from four orthogonal layers, each addressing a distinct failure mode of trend-following.

## Mechanics

### Tranching

Four weekly tranches, each rebalancing on a different week of the month. The portfolio is the average of the four tranches' positions, so signal/regime transitions phase in across multiple weeks rather than as a single-day flip.

### Vol-regime signal switching

Each tranche selects its active trend signal based on the current vol regime:

- 20-day realized vol percentile < 0.33 (low vol): 12-month TSMOM (slow)
- 0.33 <= percentile < 0.67 (medium vol): 6-month TSMOM (medium)
- percentile >= 0.67 (high vol): 3-month TSMOM (fast)

The vol percentile is the rank of current 20-day realized vol within the trailing 252-day distribution.

### Equity sleeve from tranche agreement

Allocation is driven by tranche agreement count plus two protective overlays.

**Base count -> sleeve:**

| Bullish tranches | Position |
|---|---|
| 4 of 4 | 100% TQQQ (2x leveraged) |
| 3 of 4 | 100% QQQ |
| 2 of 4 | 100% BIL (cash) |
| 0-1 of 4 | 100% hedge basket |

**Asymmetric hysteresis on downgrades.** Once in a higher-leverage sleeve,
the bullish count must drop further than the entry threshold before the
strategy demotes. Specifically: TQQQ holds at 4/4 OR 3/4 (only demotes at
2/4); QQQ holds at 3/4 OR 2/4 (only demotes at 1/4); cash holds at 2/4 OR
1/4 (only demotes to hedge at 0/4). This prevents chop years from
round-tripping leveraged ETFs at every 4->3 oscillation.

**Vol-regime leverage cap.** When the realized-vol percentile is in the top
third (>=0.67), TQQQ is capped at QQQ regardless of bullish count. The 1x
sleeve and the hedge sleeve are not capped. This keeps the strategy from
holding leveraged equity during high-volatility regimes (the period most
hostile to TQQQ's path-dependency cost).

### Hedge basket

Activated when 0 or 1 tranches are bullish on equity. Default universe: {TLT, GLD, BIL}. Each non-cash hedge slot fills if its trailing 6-month return > 0; failed slots collapse to BIL.

DBMF (managed futures) was considered for the hedge basket but materially hurt performance on the 2021-2026 sample (every risk metric got worse) while also restricting backtestable history to 2020+ due to its May 2019 inception. It can be added via `--hedges TLT,GLD,DBMF,BIL` for users who want managed-futures exposure, but the default is intentionally TLT,GLD,BIL.

### Vol kill switch

Force-exits equity to cash when realized vol explodes (vol_ratio = current 20-day vol / trailing 252-day median > 2.0 for two consecutive days). Re-enters when vol_ratio < 1.5 for 10 consecutive trading days.

## Universe

- **Equity sleeve (default):** TQQQ (2x leveraged Nasdaq-100), QQQ (1x Nasdaq-100), BIL (cash). Set `Levered=TQQQ` for the 3x variant; doing so restricts backtestable history to mid-2011+ due to TQQQ's Feb 2010 inception.
- **Hedge sleeve (default):** TLT (long Treasuries), GLD (gold), BIL (cash).

## References

- Hurst, Ooi, Pedersen "A Century of Evidence on Trend-Following Investing" (AQR, 2017)
- Asness, Moskowitz, Pedersen "Time Series Momentum" (Journal of Financial Economics, 2012)
- Hoffstein/Newfound Research "Tactical Asset Allocation Through Risk Cycles"
- Antonacci "Dual Momentum Investing" (2014)
- Faber "A Quantitative Approach to Tactical Asset Allocation" (2007)
