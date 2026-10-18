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
| `binance_451.json` | Binance Futures | `https://fapi.binance.com/fapi/v1/ticker/24hr?symbol=BTCUSDT` | Full HTTP 451 restricted-location payload (219 B) | Public market data |
| `binance_fapi_ticker24h.json` | Binance USD-M | `https://fapi.binance.com/fapi/v1/ticker/24hr?symbol=BTCUSDT` | Single BTCUSDT 24h ticker (`quoteVolume: "10966095175.28"`) (345 B) | Public market data |
| `binance_fapi_oi.json` | Binance USD-M | `https://fapi.binance.com/fapi/v1/openInterest?symbol=BTCUSDT` | Single BTCUSDT OI (`openInterest: "93969.124"` BTC) (92 B) | Public market data |
| `binance_fapi_premium.json` | Binance USD-M | `https://fapi.binance.com/fapi/v1/premiumIndex?symbol=BTCUSDT` | Single BTCUSDT mark/index/funding (`lastFundingRate: "0.00005447"`) (248 B) | Public market data |
| `binance_dapi_ticker24h.json` | Binance COIN-M | `https://dapi.binance.com/dapi/v1/ticker/24hr?symbol=BTCUSD_PERP` | Single BTCUSD_PERP 24h ticker (`volume` in $100 contracts) (355 B) | Public market data |
| `binance_dapi_oi.json` | Binance COIN-M | `https://dapi.binance.com/dapi/v1/openInterest?symbol=BTCUSD_PERP` | Single BTCUSD_PERP OI (`openInterest: "13438624"` × $100 contracts) (136 B) | Public market data |
| `okx_swap_ticker.json` | OKX | `https://www.okx.com/api/v5/market/ticker?instId=BTC-USDT-SWAP` | Single BTC-USDT-SWAP ticker (`volCcy24h: "69758.85"` BTC) (309 B) | Public market data |
| `okx_swap_oi.json` | OKX | `https://www.okx.com/api/v5/public/open-interest?instType=SWAP&instId=BTC-USDT-SWAP` | Single BTC-USDT-SWAP OI (`oiCcy: "42500.0"`, `oiUsd: "3463070000.00"`) (209 B) | Public market data |
| `okx_swap_funding.json` | OKX | `https://www.okx.com/api/v5/public/funding-rate?instId=BTC-USDT-SWAP` | Single BTC-USDT-SWAP funding (`fundingTime` / `nextFundingTime` = 8 h interval) (255 B) | Public market data |
| `coinbase_intx_btc_perp.json` | Coinbase INTX | `https://api.international.coinbase.com/api/v1/instruments/BTC-PERP` | Single `BTC-PERP` instrument (`open_interest: "1031.39"`, `notional_24hr: "0"`) (622 B) | Public market data |
| `coinbase_cde_bip.json` | Coinbase CDE | `https://api.coinbase.com/api/v3/brokerage/market/products/BIP-20DEC30-CDE` | Single `BIP-20DEC30-CDE` product (`contract_size: "0.01"` BTC, `open_interest: "173743"`, `funding_interval: "3600s"`) (539 B) | Public market data |
| `kraken_futures_tickers.json` | Kraken Futures | `https://futures.kraken.com/derivatives/api/v3/tickers` | Trimmed to `PF_XBTUSD` perp and `FF_XBTUSD_261225` dated future (685 B) | Public market data |
| `hyperliquid_meta_and_ctx.json` | Hyperliquid | `POST https://api.hyperliquid.xyz/info` (`{"type":"metaAndAssetCtxs"}`) | Trimmed to first 2 universe assets (`BTC`, `ETH`) and matching asset contexts (669 B) | Public market data |
| `deribit_perp_ticker.json` | Deribit | `https://www.deribit.com/api/v2/public/ticker?instrument_name=BTC-PERPETUAL` | Single `BTC-PERPETUAL` ticker (`open_interest` and `stats.volume_usd` in USD) (315 B) | Public market data |
| `deribit_futures_summary.json` | Deribit | `https://www.deribit.com/api/v2/public/get_book_summary_by_currency?currency=BTC&kind=future` | Trimmed to `BTC-PERPETUAL` + 3 dated expiries (`BTC-30OCT26`, `BTC-27NOV26`, `BTC-25DEC26`) (769 B) | Public market data |
| `deribit_dvol.json` | Deribit | `https://www.deribit.com/api/v2/public/get_volatility_index_data?currency=BTC&resolution=86400` | 3 daily `[ts_ms, open, high, low, close]` DVOL candles (235 B) | Public market data |

## 1.1 Hand-Checked Venue Semantics (Task 4.1)

- **Coinbase INTX (`BTC-PERP`):** `notional_24hr` and `qty_24hr` report the current rolling 24h window and can legitimately return `"0"` while `avg_daily_notional` / `avg_daily_qty` are non-zero 30d averages. When `notional_24hr == 0`, the adapter sets INTX 24h volume to `nil` (never substituting `avg_daily_notional`).
- **Coinbase CDE (`BIP-20DEC30-CDE`):** `future_product_details.contract_size` is `"0.01"` BTC (`open_interest: "173743"` contracts = `1,737.43 BTC`; `volume_24h` is in 0.01 BTC contracts). `funding_interval` is `"3600s"` (1 hour), so `funding_rate: "0.00001"` annualizes as `0.00001 × 8760 × 100 = 8.76 %` APR.

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
