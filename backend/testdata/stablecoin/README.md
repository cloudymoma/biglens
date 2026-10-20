# Stablecoin Dashboard Test Fixtures (synthetic)

**Every value in these files is made up.** The files reproduce the *shape* of the upstream responses that were captured
on 2026-10-19 (M0) for the design in the untracked `stablecoin.md` (§4, §13, Appendix E): field names, JSON types
(string vs integer vs float, including mixed types), nesting, key order and the upstream quirks listed in §2. A
key-path and JSON-type comparison against the captured responses found 0 differences in all 22 JSON files.

Kept because they are identifiers or public facts, not provider data: chain names and codes, pair and product names,
contract and pool addresses, dates, the Tether update id (`1792193400` = 2026-10-16T23:30:00Z) and the SDMX codelist
ids. The repository is public, and the captured responses may not be republished (e.g. DefiLlama, `stablecoin.md`
§8.7), so the real values appear only as cited facts in §3.

`generate.py` writes every file here and `../../opendata_stablecoin_fixtures_test.go` (the `fx…` Go constants the tests
assert against), and checks the replay shapes below. Don't hand-edit the fixtures; change the script and run
`python3 generate.py` from this directory.

## 1. Inventory

| File | Mimics | Contents |
|---|---|---|
| `defillama_stablecoins.json` | `stablecoins.llama.fi/stablecoins?includePrices=true` | USDT (id 1) and USDC (id 2) with their top-6 chains; EURC (id 50) with all 13 chains; EURT (dead, `price: null`), EURS (`peggedEUR`) and VOLT (`peggedVAR`, neither USD nor EUR) |
| `defillama_charts_usdt.json`, `defillama_charts_eurc.json` | `stablecoins.llama.fi/stablecoincharts/all?stablecoin={1,50}` | 5 daily rows, 2026-10-15..19; settled days are integers, the last day is floats plus `totalMintedUSD`/`totalBridgedToUSD` |
| `circle_stablecoins.json` | `api.circle.com/v1/stablecoins` | USDC on 39 chains, EURC on 9 chains, string amounts, per-chain `updateDate` |
| `tether_transparency.json` | `app.tether.to/transparency.json` | `data` (legacy per-chain keys) and `data_formatted` (14 `blockChains`) for USD₮, XAU₮ and MXN₮; Σ(totalAuthorized − notIssued − quarantined) = USD₮ `total_liabilities` exactly |
| `kraken_ticker.json` | `api.kraken.com/0/public/Ticker?pair=USDTUSD,USDCUSD,EURCEUR,EURCUSD,EURUSD` | one batched response |
| `coinbase_tickers.json` | `api.exchange.coinbase.com/products/{id}/ticker` | USDT-USD, USDT-USDC, EURC-USDC, and the USDC-USD 404 |
| `bitstamp_tickers.json` | `www.bitstamp.net/api/v2/ticker/{pair}/` | usdtusd, usdcusd, eurceur (thin book), eurusd, and the unknown-pair list response |
| `okx_ticker.json`, `binance_ticker24h.json` | OKX `market/ticker?instId=USDC-USDT`, Binance `ticker/24hr?symbol=USDCUSDT` | one ticker each, HTTP status recorded |
| `uniswap_v3_usdc_usdt_rpc.json` | `eth_call` on pool `0x3416cf6c708da44db2624d63ea0aaef7113527c6` | token0(), token1(), fee(), slot0(); price 1.00065 USDT per USDC |
| `chainlink_feeds_rpc.json`, `curve_3pool.json`, `blockscout_usdc_holders.json` | Chainlink feeds, Curve `getPools/ethereum/main`, Blockscout holders | v1.1 sources (not used yet) |
| `ecb_exr_usd.json`, `ecb_estr.json` | ECB SDMX-JSON `EXR/D.USD.EUR.SP00.A`, `EST/B.EU000A2X2A25.WT` | last 3 observations; full SDMX structure |
| `treasury_bill_rates.csv` | US Treasury daily bill rates CSV | header plus 5 rows, newest first |
| `replay/*` | DefiLlama charts and prices, Bitstamp daily OHLC | scenario series, see §3 |

## 2. Traps reproduced on purpose

| Trap | Fixture | What a parser must do |
|---|---|---|
| Bitstamp answers an unknown pair with HTTP 200 and the full ticker **list** (276 entries upstream; 3 kept, `body_len` records 276) | `bitstamp_tickers.json` → `eurcusd_unknown_pair` | Require an object with `bid`/`ask`/`last`; a list is `bad_response` |
| Kraken renames result keys (`USDTUSD` → `USDTZUSD`, `EURUSD` → `ZEURZUSD`) | `kraken_ticker.json` | Map by the returned key, not the requested pair |
| Coinbase has no `USDC-USD` book: **HTTP 404** | `coinbase_tickers.json` → `USDC-USD` | A 404 on a known-missing product is `disabled`, not an outage |
| Tether amounts are strings, integers or floats, varying by coin and chain | `tether_transparency.json` | Accept every JSON number form and numeric strings |
| DefiLlama list values are USD for **every** peg type (value = native × same-response `price`) | `defillama_stablecoins.json` | Normalize: native = value ÷ `price`. Units evidence: each core coin's `circulatingPrevDay` ÷ `price` equals its chart's native total for 2026-10-18 |
| DefiLlama charts `totalCirculating` is **native** | `defillama_charts_*.json` | Use as native; `totalCirculatingUSD` is USD |
| A dead coin with `price: null` | `defillama_stablecoins.json` (EURT) | Native amounts unknown (null), USD kept |
| A peg that is neither USD nor EUR | `defillama_stablecoins.json` (VOLT, `peggedVAR`) | Excluded from the watchlist |
| Blockscout Base behind a Cloudflare challenge (HTTP 403, HTML body) | `blockscout_usdc_holders.json` → `base` | A non-JSON 403 is `blocked`, never parsed |
| Blockscout tag meta key with a trailing space (`"tagIcon "`), null `metadata` | `blockscout_usdc_holders.json` → `eth` | Don't depend on optional metadata |
| Binance geo-block path | `binance_ticker24h.json` (HTTP 200 at capture) | Reuse `../bitcoin/binance_451.json` for the 451 path |

## 3. Replay scenarios (`replay/`)

Short synthetic series that reproduce the shapes measured on the real data. `TestStableReplayFixtureShapes` pins them;
`TestStableReplayS4Scenarios` pins the inputs of the S4 flows signal (24h and 7d supply changes) on the days that decide
it.

| File | Window | Shape reproduced |
|---|---|---|
| `charts_usdt_2022-05.json` | 2022-04-28..06-15 | worst 24h −3.88% on 05-12; worst 7d −10.23% on 05-18 |
| `charts_usdc_2023-03.json` | 2023-03-01..31 | worst 24h −3.65% on 03-12; worst 7d −15.17% on 03-17 |
| `charts_usdt_2024.json` | 2024-11-20..12-31 | one-day glitch −1.38% on 12-01, rebound the next day; worst 7d −1.21% on 12-31 |
| `charts_usdc_2024.json` | 2024-04-01..26 | the measured 24h changes (0.001% precision) on a made-up level: an outflow of about 3% a week on 04-09..13, then a five-day dip and rebound, −6.483% on 04-18, +8.591%, −4.450%, +0.169%, +7.284% on 04-22; worst 7d −5.35% on 04-18 |
| `prices_2022-05.json` | 2022-04-28..06-15 | tether in 05-09..16: 0 bps through 05-14, −10.54 bps on 05-15; the only day ≥ 30 bps is +32.7 bps on 05-01 |
| `prices_2023-03.json` | 2023-03-01..31 | usd-coin 0.99755917 on 03-11, 0.961065 on 03-12, 0.991923 on 03-13; days ≥ 30 / ≥ 100 bps: usd-coin 10 / 1, tether 15 / 2 |
| `prices_2024.json` | 2024, sparse | days ≥ 30 bps: tether 10, usd-coin 14 (max 60 and 80 bps); none ≥ 100 bps |
| `bitstamp_ohlc_usdtusd_2022-05.json` | 8 days from 2022-05-09 | low 0.91011 on 05-12 (open 0.99523, high 0.99939, close 0.99675) |
| `bitstamp_ohlc_usdcusd_2023-03.json` | 8 days from 2023-03-08 | low 0.80526 on 03-11 (open 0.99729, high 0.99975, close 0.96853) |

### Measured on the real data (cited facts)

Sources: DefiLlama `stablecoincharts/all` and `stablecoinprices`, Bitstamp `ohlc/{pair}` daily candles, fetched
2026-10-19.

- **USDT, May 2022.** DefiLlama daily price over 05-09..05-16: 0 bps through 05-14, −10.54 bps on 05-15, −8.82 bps on
  05-16. Over the whole 2022-04-15..06-15 window the extremes are +32.7 bps on 05-01 (a premium, the only day ≥ 30 bps)
  and −17 bps on 06-14. Bitstamp daily low 0.91011 on 05-12. Supply: worst 24h −3.876% on 05-12, worst 7d −10.225% on
  05-18.
- **USDC, March 2023.** DefiLlama daily price 0.99755917 on 03-11, 0.961065 on 03-12 (−389 bps), 0.991923 on 03-13.
  Bitstamp daily low 0.80526 on 03-11. Supply: worst 24h −3.647% on 03-12, worst 7d −15.172% on 03-17. USDT traded at a
  premium from 03-13 to 03-26 (+100 bps on 03-13, +110 bps on 03-21).
- **2024.** USDT days off peg by ≥ 30 bps: 02-15 +40, 08-08 +60, 10-07 −33.5, 10-25 −46.58, 11-11 +50, plus five days at
  exactly +30.0 bps (01-02, 03-20, 07-13, 07-20, 10-06). USDC: 01-10 −30.4, 02-15 +50, 03-05 −30.5, 03-07 −34.8, 03-20
  +60, 08-08 +50, 10-25 −38.63, 11-11 +80, 11-26 −31.5, plus five days at exactly +30.0 bps (01-02, 03-01, 03-15, 07-13,
  11-25). None ≥ 100 bps.
- **Exact 30.0 bps.** A price of 1.003 is 30.0 bps off peg, but `1.003 − 1` is 29.99999999999989 bps in float64. A naive
  `>= 30` comparison misses those days and gives USDT 5 / USDC 9 for 2024 and 13 / 9 for March 2023, the counts quoted
  in `stablecoin.md` §13. Counting them gives 10 / 14 and 15 / 10. Both are pinned in the replay test.
- **2024 supply-chart glitches.** USDT −1.379% on 2024-12-01 with a rebound (worst 7d −1.21% on 12-31); USDC −6.483% on
  2024-04-18, +8.591% on 04-19 (worst 7d −5.35% on 04-18).
- **S4 inputs (fetched 2026-10-20).** Day after each drop, 7d change: USDT 2022-05-13 −4.545% (24h 0.000%); USDC
  2023-03-13 −6.945% (24h −0.473%); USDT 2024-12-02 +0.039% (no 7d change ≤ −2% in 2024-11-20..12-31). USDC April 2024
  (24h / 7d): 04-11 −0.312% / −2.104%, 04-12 +0.100% / −2.435%, 04-13 −0.904% / −2.998%, 04-14 +1.242% / −1.868%, 04-17
  +0.614% / +0.895%, 04-18 −6.483% / −5.350%, 04-19 +8.591% / +2.679%, 04-20 −4.450% / −0.996%, 04-21 +0.169% / −2.045%,
  04-22 +7.284% / +5.284%. The USDC dip is not one day long: §6.2's S4 rule as written ("a level counts only if the next
  input still shows a 7d change ≤ −2%") drops 04-18 but keeps 04-20's −4.450% (24h critical), because 04-21's 7d change is
  −2.045%.
