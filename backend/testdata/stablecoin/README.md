# Stablecoin Dashboard Test Fixtures (M0)

Captured on `2026-10-19` between 13:00 and 14:00 UTC (`User-Agent: biglens/1.0`) for the design in the untracked
`stablecoin.md` (§4, §13, Appendix E). Each file preserves the exact upstream JSON shape; trimming only removes array
elements or object members, never renames or retypes fields. All files are ≤ 64 KB. These fixtures are for offline
tests only; BigLens is personal-use (see `stablecoin.md` §4.6) and the data is not redistributed.

## 1. Inventory

| File | Upstream URL | Trimming rule (size) | Terms |
|---|---|---|---|
| `defillama_stablecoins.json` | `https://stablecoins.llama.fi/stablecoins?includePrices=true` | USDT(1)/USDC(2) top-6 chains; EURC(50) all chains (units evidence §5.1); 2 other peggedEUR + 1 non-USD/EUR peg (watchlist exclusion test) (14.3 KB) | test use only |
| `defillama_charts_eurc.json` | `https://stablecoins.llama.fi/stablecoincharts/all?stablecoin=50` | last 5 daily rows (1.1 KB) | test use only |
| `defillama_charts_usdt.json` | `https://stablecoins.llama.fi/stablecoincharts/all?stablecoin=1` | last 5 daily rows (1.1 KB) | test use only |
| `replay/charts_usdt_2022-05.json` | `https://stablecoins.llama.fi/stablecoincharts/all?stablecoin=1` | 2022-04-15..2022-06-15 (USDT redemption wave) (10.9 KB) | test use only |
| `replay/charts_usdc_2023-03.json` | `https://stablecoins.llama.fi/stablecoincharts/all?stablecoin=2` | 2023-02-15..2023-03-31 (USDC/SVB) (8.9 KB) | test use only |
| `replay/charts_usdt_2024.json` | `https://stablecoins.llama.fi/stablecoincharts/all?stablecoin=1` | 2024 quiet year; keys date,totalCirculating,totalCirculatingUSD only (53.1 KB) | test use only |
| `replay/charts_usdc_2024.json` | `https://stablecoins.llama.fi/stablecoincharts/all?stablecoin=2` | 2024 quiet year; keys date,totalCirculating,totalCirculatingUSD only (52.5 KB) | test use only |
| `replay/prices_2022-05.json` | `https://stablecoins.llama.fi/stablecoinprices` | 2022-04-15..2022-06-15; tether/usd-coin/euro-coin only (6.2 KB) | test use only |
| `replay/prices_2023-03.json` | `https://stablecoins.llama.fi/stablecoinprices` | 2023-02-15..2023-03-31; tether/usd-coin/euro-coin only (5.3 KB) | test use only |
| `replay/prices_2024.json` | `https://stablecoins.llama.fi/stablecoinprices` | 2024; tether/usd-coin only (31.6 KB) | test use only |
| `circle_stablecoins.json` | `https://api.circle.com/v1/stablecoins` | full response (7.7 KB) | Undocumented public endpoint — test use only |
| `tether_transparency.json` | `https://app.tether.to/transparency.json` | full response (mixed string/number types preserved) (11.6 KB) | Tether transparency page (outside main ToS) — test use only |
| `kraken_ticker.json` | `https://api.kraken.com/0/public/Ticker?pair=USDTUSD,USDCUSD,EURCEUR,EURCUSD,EURUSD` | full (renamed result keys preserved) (2.3 KB) | Kraken terms (own use) — test use only |
| `coinbase_tickers.json` | `https://api.exchange.coinbase.com/products/{id}/ticker` | USDT-USD, USDT-USDC, EURC-USDC, plus USDC-USD (expected HTTP 400 trap) (0.8 KB) | Coinbase market data terms (personal/research) — test use only |
| `bitstamp_tickers.json` | `https://www.bitstamp.net/api/v2/ticker/{pair}/` | 4 valid pairs; eurcusd = unknown-pair trap (list trimmed to first 3, original length kept) (2.7 KB) | Bitstamp public API — test use only |
| `okx_ticker.json` | `https://www.okx.com/api/v5/market/ticker?instId=USDC-USDT` | full (0.5 KB) | OKX API agreement (personal use) — test use only |
| `binance_ticker24h.json` | `https://api.binance.com/api/v3/ticker/24hr?symbol=USDCUSDT` | full (status recorded; 451/403 = geo-block path) (0.6 KB) | Binance public API — test use only |
| `uniswap_v3_usdc_usdt_rpc.json` | `https://ethereum-rpc.publicnode.com` | eth_call token0(), token1(), fee(), slot0() on pool 0x3416cf6c708da44db2624d63ea0aaef7113527c6 (1.3 KB) | Public chain state |
| `chainlink_feeds_rpc.json` | `https://ethereum-rpc.publicnode.com` | eth_call latestRoundData(), decimals(), description() on USDC/USD, USDT/USD, EUR/USD (3.2 KB) | Public chain state |
| `curve_3pool.json` | `https://api.curve.finance/v1/getPools/ethereum/main` | poolData trimmed to 3pool only; envelope kept (2.8 KB) | Curve public API — test use only |
| `ecb_exr_usd.json` | `https://data-api.ecb.europa.eu/service/data/EXR/D.USD.EUR.SP00.A?lastNObservations=3&format=jsondata` | last 3 observations (5.4 KB) | ECB data reuse policy (attribution: "Source: ECB") |
| `ecb_estr.json` | `https://data-api.ecb.europa.eu/service/data/EST/B.EU000A2X2A25.WT?lastNObservations=3&format=jsondata` | last 3 observations (5.3 KB) | ECB data reuse policy (attribution: "Source: ECB") |
| `treasury_bill_rates.csv` | `https://home.treasury.gov/resource-center/data-chart-center/interest-rates/daily-treasury-rates.csv/2026/all?type=daily_treasury_bill_rates&field_tdr_date_value=2026&page&_format=csv` | header + first 5 rows (newest first) (0.8 KB) | US Treasury — public domain |
| `blockscout_usdc_holders.json` | `https://{eth,base}.blockscout.com/api/v2/tokens/{usdc}/holders and /tokens/{usdc}` | holders items trimmed to 5; HTTP status recorded (base 403 seen in M0) (6.4 KB) | Blockscout hosted API — test use only |
| `replay/bitstamp_ohlc_usdcusd_2023-03.json` | `https://www.bitstamp.net/api/v2/ohlc/usdcusd/?step=86400&limit=8&start=1678233600` | 8 daily candles around the event (intraday lows) (1.3 KB) | Bitstamp public API — test use only |
| `replay/bitstamp_ohlc_usdtusd_2022-05.json` | `https://www.bitstamp.net/api/v2/ohlc/usdtusd/?step=86400&limit=8&start=1652054400` | 8 daily candles around the event (intraday lows) (1.3 KB) | Bitstamp public API — test use only |

## 2. Traps captured on purpose

| Trap | Fixture | What a parser must do |
|---|---|---|
| Bitstamp unknown pair returns HTTP 200 with the full ticker **list** (276 entries) | `bitstamp_tickers.json` → `eurcusd_unknown_pair` | Require a JSON object with `bid`/`ask`/`last`; a list is `bad_response` |
| Kraken renames result keys (`USDTUSD` → `USDTZUSD`, `EURUSD` → `ZEURZUSD`) | `kraken_ticker.json` | Map by returned key, not requested pair |
| Coinbase has no `USDC-USD` book: **HTTP 404** (not 400) | `coinbase_tickers.json` → `USDC-USD` | Treat 4xx on a known-missing product as `disabled`, not an outage |
| Tether mixed string/number amounts | `tether_transparency.json` | Accept both JSON string and number |
| DefiLlama `peggedEUR` list values are **USD** | `defillama_stablecoins.json` (id 50) | Normalize: native = value ÷ same-response `price` (§5.1) |
| DefiLlama charts `totalCirculating` is **native** | `defillama_charts_eurc.json` | Use as native; `totalCirculatingUSD` is USD |
| Non-USD/EUR peg in the list | `defillama_stablecoins.json` (last asset) | Excluded from the watchlist |
| Blockscout Base behind a Cloudflare challenge (HTTP 403, HTML body) | `blockscout_usdc_holders.json` → `base` | Non-JSON 403 is `blocked`, never parsed; source badge `error` |
| Binance geo-block path | `binance_ticker24h.json` records the HTTP status (200 at capture) | Reuse `../bitcoin/binance_451.json` for the 451 path |

## 3. Replay windows (`replay/`)

DefiLlama daily data (`charts_*`, `prices_*`) plus Bitstamp daily OHLC for intraday lows. Observed values used by the
§13 replay assertions:

| Event | DefiLlama daily | Bitstamp daily low | Flows (DefiLlama `totalCirculating`) |
|---|---|---|---|
| USDT 2022-05 | worst −11 bps (2022-05-15) — **does not show the depeg** | 0.91011 on 2022-05-12 | worst 24h −3.88% (05-12), worst 7d −10.23% (05-18) |
| USDC 2023-03 | 0.99756 on 03-11, **0.96107 on 03-12** | 0.80526 on 2023-03-11 | worst 24h −3.65% (03-12), worst 7d −15.17% (03-17) |
| 2024 (quiet) | days ≥ 30 bps: USDT 5, USDC 9; ≥ 100 bps: 0; max 60 / 80 bps | — | — |
