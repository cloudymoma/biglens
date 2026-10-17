# Bitcoin Dashboard Test Fixtures (M0–M3 Scope)

Captured on `2026-10-17T09:28:28Z` (`User-Agent: biglens/1.0`). Each fixture is trimmed to ≤ 64 KB while preserving the exact JSON shape returned by the upstream API.

## 1. Fixture Inventory (M0–M3 Providers)

| File | Provider | Upstream URL | Trimming Rule | License / Attribution |
|---|---|---|---|---|
| `mempool_difficulty_adjustment.json` | mempool.space | `https://mempool.space/api/v1/difficulty-adjustment` | Full response (397 B) | Open / fair use |
| `mempool_pools_1w.json` | mempool.space | `https://mempool.space/api/v1/mining/pools/1w` | First 12 entries of `pools[]` + `blockCount` + `lastEstimatedHashrate` (3.6 KB) | Open / fair use |
| `mempool_statistics_24h.json` | mempool.space | `https://mempool.space/api/v1/statistics/24h` | First 6 time slices, each retaining its full 39-element `vsizes` array (3.7 KB) | Open / fair use |
| `mempool_block_fee_rates_1w.json` | mempool.space | `https://mempool.space/api/v1/mining/blocks/fee-rates/1w` | First 10 entries (`avgHeight`, `timestamp`, `avgFee_0`..`avgFee_100`) (2.0 KB) | Open / fair use |
| `mempool_reward_stats_144.json` | mempool.space | `https://mempool.space/api/v1/mining/reward-stats/144` | Full response (131 B) | Open / fair use |
| `mempool_prices.json` | mempool.space | `https://mempool.space/api/v1/prices` | Full response (142 B) | Open / fair use |
| `coinbase_stats.json` | Coinbase Exchange | `https://api.exchange.coinbase.com/products/BTC-USD/stats` | Full response (230 B) | Public market data |
| `coinbase_candles.json` | Coinbase Exchange | `https://api.exchange.coinbase.com/products/BTC-USD/candles?granularity=86400` | First 10 daily `[time, low, high, open, close, volume]` candles (985 B) | Public market data |
| `kraken_ohlc.json` | Kraken | `https://api.kraken.com/0/public/OHLC?pair=XBTUSD&interval=1440` | Last 10 daily OHLC rows + `last` cursor (1.8 KB) | Public market data |
| `coinmetrics_asset_metrics.json` | Coin Metrics Community | `https://community-api.coinmetrics.io/v4/timeseries/asset-metrics?assets=btc&metrics=CapMVRVCur,SplyCur,FlowInExUSD,FlowOutExUSD,SplyExNtv,IssTotNtv,FeeTotNtv,AdrActCnt,TxCnt,PriceUSD&frequency=1d&page_size=10&start_time=2026-10-05` | 10 daily rows + `next_page_token` / `next_page_url` (8.2 KB) | CC BY-NC 4.0 (attribution required; non-commercial) |
| `coingecko_treasuries.json` | CoinGecko | `https://api.coingecko.com/api/v3/companies/public_treasury/bitcoin` | Top 25 companies in `companies[]` + aggregate totals (6.8 KB) | Public API (attribution required) |
| `blockchain_market_price.json` | blockchain.com | `https://api.blockchain.info/charts/market-price?timespan=all&sampled=false&format=json` | 3 pre-2010-08-18 zero rows + 10 rows starting `2010-08-18` + 6 rows in `2015-07` overlapping Coinbase start (1.2 KB) | Public chart data |

Fixtures for M4–M5 providers (Deribit, OKX, Binance, Hyperliquid, CoinGecko `/derivatives` and curated spot tickers, CFTC, alternative.me, Bitnodes, DB-IP) are added by their respective M4–M5 tasks if G1 approves M4–M6.

## 2. `mempool.space` 39-Entry Fee Levels (`mempoolStatsFeeLevels`)

Source: [`frontend/src/app/app.constants.ts`](https://github.com/mempool/mempool/blob/master/frontend/src/app/app.constants.ts) in `mempool/mempool`:

```ts
export const feeLevels = [
  0, 1, 2, 3, 4, 5, 6, 8, 10, 12, 15, 20, 30, 40, 50, 60, 70, 80, 90, 100,
  125, 150, 175, 200, 250, 300, 350, 400, 500, 600, 700, 800, 900, 1000,
  1200, 1400, 1600, 1800, 2000,
];
```

Each element `vsizes[i]` in `/api/v1/statistics/{24h,1w,1m}` represents the mempool virtual bytes in bucket `feeLevels[i]` (where index `i` covers `[feeLevels[i], feeLevels[i+1])` sat/vB, and index 38 covers `>= 2000` sat/vB). `foldMempoolStats` folds the 39 entries (or up to `min(len(vsizes), len(mempoolStatsFeeLevels))`) into 6 display bands:
- `0–1 sat/vB`: `feeLevels[i] < 1` (index 0: `0`)
- `1–2 sat/vB`: `1 <= feeLevels[i] < 2` (index 1: `1`)
- `2–5 sat/vB`: `2 <= feeLevels[i] < 5` (indices 2..4: `2, 3, 4`)
- `5–10 sat/vB`: `5 <= feeLevels[i] < 10` (indices 5..7: `5, 6, 8`)
- `10–50 sat/vB`: `10 <= feeLevels[i] < 50` (indices 8..13: `10, 12, 15, 20, 30, 40`)
- `50+ sat/vB`: `feeLevels[i] >= 50` (indices 14..38: `50..2000`)
