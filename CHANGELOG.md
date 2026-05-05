# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] - 2026-05-04

### Changed
- Default benchmark from VFINX to SPY
- Upgrade pvbt dependency to v0.9.2

## [0.1.0] - 2026-05-03

### Added
- Initial release of Leveraged Trend Ensemble strategy
- Four-tranche weekly rebalance averaged into the portfolio
- Vol-regime trend signal (fast/medium/slow) with tranche-agreement leverage ladder (QLD / QQQ / cash / hedge)
- Asymmetric hysteresis on leverage downgrades and a vol-regime cap on QLD to suppress chop-year round-trips
- Vol kill switch forces equity exit when realized vol clears the configured threshold
- TSMOM-filtered hedge basket (default `TLT,GLD,BIL`) with per-slot 6-month return filter; failed slots collapse to cash

[0.1.0]: https://github.com/penny-vault/leveraged-trend-ensemble/releases/tag/v0.1.0
[0.2.0]: https://github.com/penny-vault/leveraged-trend-ensemble/compare/v0.1.0...v0.2.0
