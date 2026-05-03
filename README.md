# Leveraged Trend Ensemble

A leveraged-equity-with-dynamic-hedge strategy built from four orthogonal layers, each addressing a distinct failure mode of trend-following. Targets the 2022 case specifically (where most leveraged strategies failed) by routing the hedge through its own TSMOM filter rather than a fixed bond allocation.

See [DESIGN.md](./DESIGN.md) for the full design rationale.

## Quick start

```bash
make build
./leveraged-trend-ensemble describe
./leveraged-trend-ensemble backtest --start 2010-01-01 --end 2026-04-30
```

## Layers

1. **Tranching** — 4 weekly tranches, portfolio = average of tranches.
2. **Vol-regime signal switching** — fast/medium/slow trend signal selected by current vol percentile.
3. **Tranche agreement -> leverage** — QLD (4/4), QQQ (3/4), cash (2/4), hedge (0-1/4). Asymmetric hysteresis on downgrades and a vol-regime cap on QLD prevent chop-year round-trips.
4. **Vol kill switch** — extreme realized vol forces equity exit regardless of trend.

## Hedge basket

When the trend ensemble is bearish on equity, the portfolio rotates into a TSMOM-filtered basket of {TLT, GLD, BIL} (default). Each hedge must clear a 6-month return > 0 filter; failed slots collapse to BIL. DBMF is available via `--hedges TLT,GLD,DBMF,BIL` but is not in the default — it underperforms on the post-2020 sample.
