#!/usr/bin/env python3
"""Synthetic test fixtures for the Stablecoins dashboard (stablecoin.md §4, §13).

Every number in these files is made up. Field names, JSON types, nesting and the
upstream quirks the parsers must handle are kept exactly (see README.md). Chain and
pair identifiers, contract addresses, dates and update timestamps are kept because
they are schema or public facts, not provider data.

Run from this directory:  python3 generate.py
It rewrites the fixtures here and ../../opendata_stablecoin_fixtures_test.go
(Go constants the tests assert against), and checks every replay shape that
README §3 cites.
"""
import datetime as dt
import json
import math
import os
import subprocess
from decimal import Decimal, getcontext

getcontext().prec = 50
HERE = os.path.dirname(os.path.abspath(__file__))
GO_OUT = os.path.join(HERE, "..", "..", "opendata_stablecoin_fixtures_test.go")
CONSTS = []  # (name, go literal, comment)


def const(name, value, comment=""):
    if isinstance(value, str):
        lit = json.dumps(value)
    elif isinstance(value, bool):
        lit = "true" if value else "false"
    elif isinstance(value, int):
        lit = str(value)
    else:
        lit = repr(float(value))
    CONSTS.append((name, lit, comment))
    return value


def write(rel, obj):
    path = os.path.join(HERE, rel)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        if isinstance(obj, str):
            f.write(obj)
        else:
            json.dump(obj, f, indent=1, ensure_ascii=False)
            f.write("\n")


def unix(day):
    return int(dt.datetime.strptime(day, "%Y-%m-%d").replace(tzinfo=dt.timezone.utc).timestamp())


def days(first, last):
    d = dt.date.fromisoformat(first)
    end = dt.date.fromisoformat(last)
    out = []
    while d <= end:
        out.append(d.isoformat())
        d += dt.timedelta(days=1)
    return out


def dec_str(x):
    """Plain decimal string (no exponent) for Decimal x."""
    s = format(x, "f")
    return s


# ---------------------------------------------------------------- DefiLlama list

# Totals sit close to the issuer totals below (Tether liabilities, Circle sums),
# as they do upstream, so S12 checks on the fixtures stay below the watch level.
LLAMA = {
    "USDT": dict(id="1", name="Tether", gecko="tether", peg="peggedUSD", mech="fiat-backed", price=0.99951,
                 cur=155_857_000_000.0, d1=155_820_000_000.0, d7=156_210_000_000.0, d30=154_900_000_000.0,
                 chains=[("Tron", 78_500_000_000.0), ("Ethereum", 62_300_000_000.0), ("BSC", 7_500_000_000.0),
                         ("Solana", 2_100_000_000.0), ("Arbitrum", 700_000_000.0), ("Polygon", 600_000_000.0)]),
    "USDC": dict(id="2", name="USD Coin", gecko="usd-coin", peg="peggedUSD", mech="fiat-backed", price=0.99993,
                 cur=59_950_000_000.0, d1=60_070_000_000.0, d7=60_350_000_000.0, d30=61_600_000_000.0,
                 chains=[("Ethereum", 38_400_000_000.0), ("Hyperliquid L1", 5_600_000_000.0), ("Solana", 5_500_000_000.0),
                         ("Base", 3_600_000_000.0), ("Arbitrum", 1_950_000_000.0), ("Polygon", 540_000_000.0)]),
    "EURC": dict(id="50", name="EURC", gecko="euro-coin", peg="peggedEUR", mech="fiat-backed", price=1.085,
                 cur=None, d1=383_000_000.0, d7=379_000_000.0, d30=355_000_000.0,
                 chains=[("Ethereum", 280_000_000.0), ("Base", 45_000_000.0), ("Solana", 43_000_000.0), ("Arc", 6_000_000.0),
                         ("Avalanche", 4_400_000.0), ("Stellar", 3_900_000.0), ("Tempo", 1_200_000.0), ("World Chain", 500_000.0),
                         ("Cronos", 300_000.0), ("Sonic", 9_000.0), ("Polygon", 3_000.0), ("Cardano", 150.0), ("ICP", 0.0)]),
}
# EURC lists every chain, so its total is the per-chain sum.
LLAMA["EURC"]["cur"] = sum(v for _, v in LLAMA["EURC"]["chains"])


def amt(peg, v):
    return {peg: v}


def llama_asset(sym, spec):
    p = spec["price"]
    peg = spec["peg"]
    chain_obj = {}
    for i, (name, native) in enumerate(spec["chains"]):
        # per-chain prev values drift a little (made up)
        prev_d = native * (1 + 0.001 * ((i % 3) - 1))
        prev_w = native * (1 - 0.004 * ((i % 4) - 1.5))
        prev_m = native * (1 - 0.02 * ((i % 5) - 2))
        chain_obj[name] = {
            "current": amt(peg, native * p),
            "circulatingPrevDay": amt(peg, prev_d * p),
            "circulatingPrevWeek": amt(peg, prev_w * p),
            "circulatingPrevMonth": amt(peg, prev_m * p),
        }
    return {
        "id": spec["id"], "name": spec["name"], "symbol": sym, "gecko_id": spec["gecko"], "pegType": peg,
        "priceSource": "defillama", "pegMechanism": spec["mech"],
        "circulating": amt(peg, spec["cur"] * p), "circulatingPrevDay": amt(peg, spec["d1"] * p),
        "circulatingPrevWeek": amt(peg, spec["d7"] * p), "circulatingPrevMonth": amt(peg, spec["d30"] * p),
        "chainCirculating": chain_obj, "chains": [c for c, _ in spec["chains"]], "price": p,
    }


assets = [llama_asset(s, LLAMA[s]) for s in ("USDT", "USDC", "EURC")]
# A dead EUR coin with a null price (native amounts must be null), a live EUR coin,
# and a peg type that is neither USD nor EUR (watchlist exclusion).
assets.append({"id": "49", "name": "Euro Tether", "symbol": "EURT", "gecko_id": "tether-eurt", "pegType": "peggedEUR",
               "priceSource": "defillama", "pegMechanism": "fiat-backed",
               "circulating": amt("peggedEUR", 3_900_000.0), "circulatingPrevDay": amt("peggedEUR", 3_900_000.0),
               "circulatingPrevWeek": amt("peggedEUR", 3_910_000.0), "circulatingPrevMonth": amt("peggedEUR", 3_950_000.0),
               "chainCirculating": {"Ethereum": {"current": amt("peggedEUR", 3_600_000.0), "circulatingPrevDay": amt("peggedEUR", 3_600_000.0),
                                                 "circulatingPrevWeek": amt("peggedEUR", 3_610_000.0), "circulatingPrevMonth": amt("peggedEUR", 3_640_000.0)},
                                    "Polygon": {"current": amt("peggedEUR", 300_000.0), "circulatingPrevDay": amt("peggedEUR", 300_000.0),
                                                "circulatingPrevWeek": amt("peggedEUR", 300_000.0), "circulatingPrevMonth": amt("peggedEUR", 310_000.0)}},
               "chains": ["Ethereum", "Polygon"], "deadFrom": "2025-12-15", "price": None})
EURS_CHAINS = [("Ethereum", 5_000_000.0), ("Stellar", 900_000.0), ("Polygon", 400_000.0), ("Algorand", 150_000.0),
               ("Gnosis", 50_000.0), ("Everscale", 0.0)]
assets.append({"id": "51", "name": "Stasis Euro", "symbol": "EURS", "gecko_id": "stasis-eurs", "pegType": "peggedEUR",
               "priceSource": "defillama", "pegMechanism": "fiat-backed",
               "circulating": amt("peggedEUR", 6_500_000.0 * 1.083), "circulatingPrevDay": amt("peggedEUR", 6_500_000.0 * 1.083),
               "circulatingPrevWeek": amt("peggedEUR", 6_400_000.0 * 1.083), "circulatingPrevMonth": amt("peggedEUR", 6_100_000.0 * 1.083),
               # An emptied chain reports the integer 0 upstream.
               "chainCirculating": {name: {"current": amt("peggedEUR", v * 1.083 if v else 0),
                                           "circulatingPrevDay": amt("peggedEUR", v * 1.083 if v else 0),
                                           "circulatingPrevWeek": amt("peggedEUR", (v * 0.98 if v else 1500.0) * 1.083),
                                           "circulatingPrevMonth": amt("peggedEUR", (v * 0.92 if v else 1500.0) * 1.083)}
                                    for name, v in EURS_CHAINS},
               "chains": [name for name, _ in EURS_CHAINS], "price": 1.083})
assets.append({"id": "39", "name": "VOLT Protocol", "symbol": "VOLT", "gecko_id": None, "pegType": "peggedVAR",
               "priceSource": "defillama", "pegMechanism": "algorithmic",
               "circulating": amt("peggedVAR", 70_000.0), "circulatingPrevDay": amt("peggedVAR", 70_000.0),
               "circulatingPrevWeek": amt("peggedVAR", 70_000.0), "circulatingPrevMonth": amt("peggedVAR", 70_000.0),
               "chainCirculating": {"Arbitrum": {"current": amt("peggedVAR", 63_000.0), "circulatingPrevDay": amt("peggedVAR", 63_000.0),
                                                 "circulatingPrevWeek": amt("peggedVAR", 63_000.0), "circulatingPrevMonth": amt("peggedVAR", 63_000.0)},
                                    "Ethereum": {"current": amt("peggedVAR", 7_000.0), "circulatingPrevDay": amt("peggedVAR", 7_000.0),
                                                 "circulatingPrevWeek": amt("peggedVAR", 7_000.0), "circulatingPrevMonth": amt("peggedVAR", 7_000.0)}},
               "chains": ["Arbitrum", "Ethereum"], "price": 0.9999})
write("defillama_stablecoins.json", {"peggedAssets": assets})

for sym, short in (("USDT", "USDT"), ("USDC", "USDC"), ("EURC", "EURC")):
    s = LLAMA[sym]
    const(f"fxLlama{short}Price", s["price"])
    const(f"fxLlama{short}CirculatingNative", s["cur"])
    const(f"fxLlama{short}CirculatingUSD", s["cur"] * s["price"])
    const(f"fxLlama{short}PrevDayNative", s["d1"], "equals the chart's native total for 2026-10-18 (units evidence)")
    const(f"fxLlama{short}PrevWeekNative", s["d7"])
    const(f"fxLlama{short}PrevMonthNative", s["d30"])
const("fxLlamaEURCEthereumNative", 280_000_000.0)
const("fxLlamaEURCEthereumUSD", 280_000_000.0 * LLAMA["EURC"]["price"])

# ---------------------------------------------------------------- DefiLlama charts

CHART_DAYS = ["2026-10-15", "2026-10-16", "2026-10-17", "2026-10-18", "2026-10-19"]


def chart(peg, natives, unreleased, prices):
    # Settled days carry integer amounts; the in-progress last day carries floats
    # plus two extra keys, as upstream does.
    rows = []
    for i, d in enumerate(CHART_DAYS):
        last = i == len(CHART_DAYS) - 1
        usd = round(natives[i] * prices[i], 2) if last else int(round(natives[i] * prices[i]))
        r = {"date": str(unix(d)), "totalCirculating": {peg: natives[i]}, "totalUnreleased": {peg: unreleased[i]},
             "totalCirculatingUSD": {peg: usd}}
        if last:
            r["totalMintedUSD"] = {peg: 0}
            r["totalBridgedToUSD"] = {peg: 0}
        rows.append(r)
    return rows


usdt_nat = [156_080_000_000, 155_930_000_000, 155_890_000_000, int(LLAMA["USDT"]["d1"]), 155_858_000_000.5]
write("defillama_charts_usdt.json", chart("peggedUSD", usdt_nat, [8_100_000_000, 8_150_000_000, 8_150_000_000, 8_190_000_000, 8_185_000_000.25],
                                          [0.9994, 0.99945, 0.99948, 0.9995, 0.99951]))
eurc_nat = [378_400_000, 381_900_000, 384_200_000, int(LLAMA["EURC"]["d1"]), 384_250_000.75]
eurc_px = [1.0838, 1.0846, 1.0851, 1.0849, 1.0852]
write("defillama_charts_eurc.json", chart("peggedEUR", eurc_nat, [46_000_000, 45_600_000, 44_900_000, 45_200_000, 45_150_000.5], eurc_px))
const("fxChartEURCLastNative", eurc_nat[-1])
const("fxChartEURCLastUSD", round(eurc_nat[-1] * eurc_px[-1], 2))
const("fxChartEURCLastUnreleased", 45_150_000.5)

# ---------------------------------------------------------------- Circle

USDC_CHAINS = ["ALGO", "APTOS", "ARB", "ARC", "AVAX", "BASE", "CELO", "CODEX", "CRONOS", "EDGE", "ETH", "HBAR", "HYPEREVM",
               "INJECTIVE", "INK", "LINEA", "MONAD", "MORPH", "NEAR", "NOBLE", "OP", "PAH", "PHAROS", "PLASMA", "PLUME", "POLY",
               "SEI", "SOL", "SONIC", "STRK", "SUI", "TRX", "UNI", "WORLDCHAIN", "XDC", "XLAYER", "XLM", "XRP", "ZKS"]
assert len(USDC_CHAINS) == 39
# ETH amounts are exact binary fractions so test RPC stubs can echo them losslessly.
BIG = {"ETH": Decimal("40123456789.125"), "SOL": Decimal("5512345678.5"), "HYPEREVM": Decimal("5400000000.25"),
       "BASE": Decimal("3600000000.75"), "ARB": Decimal("2210000000.123"), "ARC": Decimal("590000000.5"),
       "POLY": Decimal("540000000.25"), "XLM": Decimal("410000000.125"), "OP": Decimal("120000000.5"),
       "TRX": Decimal("25000000.5")}
UPD = ["2026-10-19T13:19:00.821Z", "2026-10-19T13:19:00.845Z", "2026-10-19T13:19:01.312Z", "2026-10-19T13:19:01.697Z"]
usdc_rows = []
for i, c in enumerate(USDC_CHAINS):
    amount = BIG.get(c, Decimal(1_000_000 + 2_345_678 * (i + 1)) + Decimal("0.123456"))
    upd = UPD[0] if c == "ETH" else UPD[(i % 3) + 1] if c != "ALGO" else UPD[3]
    usdc_rows.append({"chain": c, "amount": dec_str(amount), "secondaryMarketBurnedAmount": "12345" if c == "ETH" else "0", "updateDate": upd})
usdc_total = sum(Decimal(r["amount"]) for r in usdc_rows)
EURC_CHAINS = [("ARC", "6100000.5"), ("AVAX", "4400000.25"), ("BASE", "45100000.5"), ("CRONOS", "300000.75"), ("ETH", "280250000.125"),
               ("PLASMA", "250000.5"), ("SOL", "43800000.25"), ("WORLDCHAIN", "500000.5"), ("XLM", "3900000.125")]
eurc_rows = [{"chain": c, "amount": a, "secondaryMarketBurnedAmount": "0", "updateDate": UPD[1] if c != "ETH" else UPD[3]} for c, a in EURC_CHAINS]
eurc_total = sum(Decimal(a) for _, a in EURC_CHAINS)
write("circle_stablecoins.json", {"data": [
    {"name": "Euro Coin", "symbol": "EURC", "totalAmount": dec_str(eurc_total), "totalSecondaryMarketBurnedAmount": "0", "chains": eurc_rows},
    {"name": "USD Coin", "symbol": "USDC", "totalAmount": dec_str(usdc_total), "totalSecondaryMarketBurnedAmount": "12345", "chains": usdc_rows},
]})
const("fxCircleUSDCTotal", float(usdc_total))
const("fxCircleUSDCChains", 39)
const("fxCircleUSDCEth", float(BIG["ETH"]))
const("fxCircleUSDCEthUpdated", UPD[0])
const("fxCircleUSDCNewestUpdated", UPD[3])
const("fxCircleEURCTotal", float(eurc_total))
const("fxCircleEURCChains", 9)
const("fxCircleEURCEth", 280250000.125)
const("fxCircleEURCBase", 45100000.5)
const("fxCircleEURCSol", 43800000.25)

# ---------------------------------------------------------------- Tether

# (name, totalAuthorized, notIssued, quarantined); the JSON type of every value
# is chosen on purpose: str, float, or int.
TETHER = [
    ("Aptos", "1100000000.5", "650000000.25", 0.0),
    ("Avalanche", "1700000000.125", "1420000000.5", 0.0),
    ("BNB Smart Chain", 0, 0, 0.0),
    ("Celo", "420000000.0", "330000000.5", 0.0),
    ("Cosmos", 150000000.25, 101000000.125, 0.0),
    ("Ethereum", "75000000000.5", "1900000000.25", 0.0),
    ("Kaia", "230000000.0", "52000000.0", 0.0),
    ("Liquid", "91000000.0", "60000000.0", 0.0),
    ("Near", 500000000.5, 460000000.25, 0.0),
    ("Solana", "3100000000.5", "950000000.25", "3000000.125"),
    ("Polkadot Asset Hub", "70000000.5", "38000000.25", 0.0),
    ("Tezos", "130000000.0", "105000000.5", 0.0),
    ("Ton", "1300000000.5", "760000000.25", 0.0),
    ("Tron", 80000000000.125, 1100000000.375, 0.0),
]


def D(v):
    return Decimal(str(v))


liab = sum(D(ta) - D(ni) - D(q) for _, ta, ni, q in TETHER)
assets_usd = liab + Decimal("4100000000.125")
equity = assets_usd - liab
fmt = []
for name, ta, ni, q in TETHER:
    fmt.append({"totalAuthorized": ta, "notIssued": ni, "quarantined": q, "name": name})

# Legacy `data.<coin>` objects: totaltokens_/reserve_balance_/quarantined_ per
# legacy chain key, in upstream order. Each coin has its own upstream JSON-type
# layout (s = string, i = integer, f = float), reproduced exactly.
LEGACY_KEYS = ["algo", "aptos", "ava", "bnb", "celo", "cosmos", "eth", "eos", "kaia", "liq", "near", "omni", "sol", "slp",
               "statemine", "statemint", "tezos", "ton", "tron"]
LEGACY_TYPES = {
    "usdt": "sif ssf ssf iif ssf fff ssf ssf ssf ssf fff sss sss ssf ssf ssf ssf ssf fff",
    "xaut": "iif iif iif fif iif iif ssf ssf iif fff iif iif isf iif fif fif isf sif iif",
    "mxnt": "iif iif iif iif iif iif ssf ssf iif fff iif iif isf iif fif fif isf iif fff",
}
FORMATTED_TYPES = {  # data_formatted blockChains, in TETHER order
    "usdt": "ssf ssf iif ssf fff ssf ssf ssf fff sss ssf ssf ssf fff",
    "xaut": "iif iif fif iif iif ssf iif fff iif isf fif isf sif iif",
    "mxnt": "iif iif iif iif iif ssf iif fff iif isf fif isf iif fff",
}
LEGACY_TO_FORMATTED = {"aptos": "Aptos", "ava": "Avalanche", "bnb": "BNB Smart Chain", "celo": "Celo", "cosmos": "Cosmos",
                       "eth": "Ethereum", "kaia": "Kaia", "liq": "Liquid", "near": "Near", "sol": "Solana",
                       "statemint": "Polkadot Asset Hub", "tezos": "Tezos", "ton": "Ton", "tron": "Tron"}
USDT_LEGACY_ONLY = {"algo": ("700000.5", 0, 0.0), "eos": ("1500000.5", "1400000.25", 0.0),
                    "omni": ("800000000.5", "700000000.25", "30000000.0"), "slp": ("5000000.5", "4000000.25", 0.0),
                    "statemine": ("20000000.5", "19000000.25", 0.0)}


def typed(kind, magnitude):
    """A made-up amount of the given JSON type."""
    if kind == "i":
        return int(magnitude)
    if kind == "f":
        return float(magnitude) + (0.5 if magnitude else 0.0)
    return dec_str(Decimal(int(magnitude)) + (Decimal("0.25") if magnitude else Decimal(0)))


def check_types(values, sig):
    want = {"s": str, "i": int, "f": float}
    for v, k in zip(values, sig):
        assert type(v) is want[k], (values, sig)


usdt_legacy = {"currency_iso": "usdt", "symbol": "$"}
by_name = {name: (ta, ni, q) for name, ta, ni, q in TETHER}
for key, sig in zip(LEGACY_KEYS, LEGACY_TYPES["usdt"].split()):
    vals = by_name[LEGACY_TO_FORMATTED[key]] if key in LEGACY_TO_FORMATTED else USDT_LEGACY_ONLY[key]
    check_types(vals, sig)
    usdt_legacy[f"totaltokens_{key}"], usdt_legacy[f"reserve_balance_{key}"], usdt_legacy[f"quarantined_{key}"] = vals
usdt_legacy.update({"total_assets": dec_str(assets_usd), "total_liabilities": dec_str(liab + Decimal("70000000.5")),
                    "shareholder_eq": dec_str(equity), "id": 1792193400})
for (name, *vals), sig in zip(TETHER, FORMATTED_TYPES["usdt"].split()):
    check_types(vals, sig)


def minor_coin(iso, symbol, total, scale):
    legacy = {"currency_iso": iso, "symbol": symbol}
    for i, (key, sig) in enumerate(zip(LEGACY_KEYS, LEGACY_TYPES[iso].split())):
        ta = scale * (i + 1) if i % 4 else 0
        legacy[f"totaltokens_{key}"] = typed(sig[0], ta)
        legacy[f"reserve_balance_{key}"] = typed(sig[1], ta // 2)
        legacy[f"quarantined_{key}"] = typed(sig[2], 0)
    legacy.update({"total_assets": total, "total_liabilities": total, "shareholder_eq": "0.0", "id": 1792193400})
    chains = []
    for i, ((name, *_), sig) in enumerate(zip(TETHER, FORMATTED_TYPES[iso].split())):
        ta = scale * (i + 2) if i % 3 else 0
        chains.append({"totalAuthorized": typed(sig[0], ta), "notIssued": typed(sig[1], ta // 3),
                       "quarantined": typed(sig[2], 0), "name": name})
    return legacy, chains


xaut_legacy, xaut_chains = minor_coin("xaut", "℥", "700000.5", 1000)
mxnt_legacy, mxnt_chains = minor_coin("mxnt", "$", "15000000.0", 25000)
small = lambda name, iso, sym, total, chains: {"id": 1792193400, "name": name, "iso": iso, "symbol": sym, "total_assets": total,
                                              "total_liabilities": total, "shareholder_eq": "0.0", "blockChains": chains}
write("tether_transparency.json", {
    "data": {"usdt": usdt_legacy, "xaut": xaut_legacy, "mxnt": mxnt_legacy},
    "data_formatted": [
        {"id": 1792193400, "name": "USD₮", "iso": "usdt", "symbol": "$", "total_assets": dec_str(assets_usd),
         "total_liabilities": dec_str(liab), "shareholder_eq": dec_str(equity), "blockChains": fmt},
        small("XAU₮", "xaut", "℥", "700000.5", xaut_chains), small("MXN₮", "mxnt", "$", "15000000.0", mxnt_chains),
    ],
})
const("fxTetherUpdateID", 1792193400)
const("fxTetherAssetsUSD", float(assets_usd))
const("fxTetherLiabilitiesUSD", float(liab))
const("fxTetherLiabilitiesStr", dec_str(liab), "exact JSON string, for edits in tests")
const("fxTetherEquityUSD", float(equity))
const("fxTetherChains", len(TETHER))
for name, key in (("Ethereum", "Eth"), ("Tron", "Tron"), ("Solana", "Sol")):
    ta, ni, q = [t for t in TETHER if t[0] == name][0][1:]
    const(f"fxTether{key}Authorized", float(D(ta)))
    const(f"fxTether{key}NotIssued", float(D(ni)))
    const(f"fxTether{key}Quarantined", float(D(q)))
    const(f"fxTether{key}Circulating", float(D(ta) - D(ni) - D(q)))

# ---------------------------------------------------------------- ECB (SDMX-JSON)


def sdmx(flow, title, series_dims, series_attrs, obs_attrs, attr_index, values, obs_days):
    """SDMX-JSON 1.0 data message with one series. series_attrs/obs_attrs are
    (id, [value ids or None]) lists; attr_index is the series attribute vector."""
    series_key = ":".join("0" for _ in series_dims)
    obs = {str(i): [v, 0, 0] + [None] * (len(obs_attrs) - 2) for i, v in enumerate(values)}

    def attr_list(spec):
        out = []
        for aid, vals in spec:
            out.append({"id": aid, "name": aid.replace("_", " ").title(),
                        "values": [({"id": v, "name": "synthetic " + v} if v is not None else {"name": "synthetic"}) for v in vals]})
        return out

    return {
        "header": {"id": "00000000-0000-4000-8000-000000000000", "test": False, "prepared": "2026-10-19T13:00:00.000+02:00",
                   "sender": {"id": "ECB"}},
        "dataSets": [{"action": "Replace", "validFrom": "2026-10-19T13:00:00.000+02:00",
                      "series": {series_key: {"attributes": attr_index, "observations": obs}}}],
        "structure": {
            "links": [{"title": title, "rel": "dataflow", "href": f"https://data-api.ecb.europa.eu:443/service/dataflow/ECB/{flow}/1.0"}],
            "name": title,
            "dimensions": {
                "series": [{"id": k, "name": k.replace("_", " ").title(), "values": [{"id": v, "name": "synthetic " + v}]} for k, v in series_dims],
                "observation": [{"id": "TIME_PERIOD", "name": "Time period or range", "role": "time",
                                 "values": [{"id": d, "name": d, "start": d + "T00:00:00.000+02:00", "end": d + "T23:59:59.999+02:00"}
                                            for d in obs_days]}],
            },
            "attributes": {"series": attr_list(series_attrs), "observation": attr_list(obs_attrs)},
        },
    }


ecb_days = ["2026-10-14", "2026-10-15", "2026-10-16"]
FX = [1.08451, 1.08612, 1.08773]
ESTR = [2.15, 2.15, 2.151]
# Attribute ids are the SDMX codelist of each dataflow; [None] = a value without an id.
EXR_SERIES_ATTRS = [("TIME_FORMAT", [None]), ("BREAKS", []), ("COLLECTION", ["A"]), ("COMPILING_ORG", []), ("DISS_ORG", []),
                    ("DOM_SER_IDS", []), ("PUBL_ECB", []), ("PUBL_MU", []), ("PUBL_PUBLIC", []), ("UNIT_INDEX_BASE", [None]),
                    ("COMPILATION", []), ("COVERAGE", []), ("DECIMALS", ["4"]), ("NAT_TITLE", []), ("SOURCE_AGENCY", ["4F0"]),
                    ("SOURCE_PUB", []), ("TITLE", [None]), ("TITLE_COMPL", [None]), ("UNIT", ["USD"]), ("UNIT_MULT", ["0"])]
EXR_OBS_ATTRS = [("OBS_STATUS", ["A"]), ("OBS_CONF", ["F"]), ("OBS_PRE_BREAK", []), ("OBS_COM", [])]
EST_SERIES_ATTRS = [("TIME_FORMAT", [None]), ("BREAKS", []), ("COMMENT_TS", []), ("COMPILING_ORG", []), ("COVERAGE", [None]),
                    ("DATA_COMP", []), ("DECIMALS", ["3"]), ("DISS_ORG", []), ("PUBL_ECB", []), ("PUBL_MU", []), ("PUBL_PUBLIC", []),
                    ("TIME_PER_COLLECT", ["A"]), ("TITLE", [None]), ("TITLE_COMPL", [None]), ("UNIT_INDEX_BASE", []),
                    ("UNIT_MEASURE", ["PC"]), ("UNIT_MULT", ["0"])]
EST_OBS_ATTRS = [("OBS_STATUS", ["A"]), ("CONF_STATUS", ["F"]), ("PRE_BREAK_VALUE", []), ("COMMENT_OBS", []),
                 ("CALCUL_START_DATE", []), ("CALCUL_END_DATE", [])]


def attr_vector(spec):
    return [0 if vals else None for _, vals in spec]


write("ecb_exr_usd.json", sdmx("EXR", "Exchange Rates", [("FREQ", "D"), ("CURRENCY", "USD"), ("CURRENCY_DENOM", "EUR"), ("EXR_TYPE", "SP00"), ("EXR_SUFFIX", "A")],
                               EXR_SERIES_ATTRS, EXR_OBS_ATTRS, attr_vector(EXR_SERIES_ATTRS), FX, ecb_days))
write("ecb_estr.json", sdmx("EST", "Euro Short-Term Rate", [("FREQ", "B"), ("BENCHMARK_ITEM", "EU000A2X2A25"), ("DATA_TYPE_EST", "WT")],
                            EST_SERIES_ATTRS, EST_OBS_ATTRS, attr_vector(EST_SERIES_ATTRS), ESTR, ecb_days))
txt = open(os.path.join(HERE, "ecb_exr_usd.json")).read()
assert txt.count("1.08451,") == 1, "the first EXR observation must be unique in the file (tests edit it)"
for i, v in enumerate(FX):
    const(f"fxECBEURUSD{i}", v)
for i, v in enumerate(ESTR):
    const(f"fxECBESTR{i}", v)

# ---------------------------------------------------------------- US Treasury CSV

header = ('Date,"4 WEEKS BANK DISCOUNT","4 WEEKS COUPON EQUIVALENT","6 WEEKS BANK DISCOUNT","6 WEEKS COUPON EQUIVALENT",'
          '"8 WEEKS BANK DISCOUNT","8 WEEKS COUPON EQUIVALENT","13 WEEKS BANK DISCOUNT","13 WEEKS COUPON EQUIVALENT",'
          '"17 WEEKS BANK DISCOUNT","17 WEEKS COUPON EQUIVALENT","26 WEEKS BANK DISCOUNT","26 WEEKS COUPON EQUIVALENT",'
          '"52 WEEKS BANK DISCOUNT","52 WEEKS COUPON EQUIVALENT"')
BILL = [("10/16/2026", 4.01), ("10/15/2026", 4.03), ("10/14/2026", 4.04), ("10/13/2026", 4.02), ("10/09/2026", 3.99)]
lines = [header]
for d, cey in BILL:
    vals = [cey - 0.07, cey - 0.01, cey - 0.06, cey, cey - 0.05, cey + 0.02, cey - 0.1, cey, cey - 0.03, cey + 0.08, cey + 0.02, cey + 0.17, cey + 0.06, cey + 0.26]
    lines.append(d + "," + ",".join(f"{v:.2f}" for v in vals))
write("treasury_bill_rates.csv", "\n".join(lines) + "\n")
for i, (d, cey) in enumerate(sorted(BILL, key=lambda r: dt.datetime.strptime(r[0], "%m/%d/%Y"))):
    const(f"fxBill13W{i}", cey, d)

# ---------------------------------------------------------------- venues

KRAKEN = {  # pair: (bid, ask, last, vol_24h_base)
    "EURCEUR": ("0.99990000", "1.00000000", "0.99995000", "250000.00000000"),
    "EURCUSD": ("1.08520000", "1.08540000", "1.08530000", "820000.00000000"),
    "ZEURZUSD": ("1.08470", "1.08480", "1.08476", "31000000.00000000"),
    "USDCUSD": ("0.99987000", "0.99988000", "0.99988000", "71000000.00000000"),
    "USDTZUSD": ("0.99921000", "0.99922000", "0.99921000", "80000000.00000000"),
}
kr = {"error": [], "result": {}}
for pair, (b, a, c, v) in KRAKEN.items():
    kr["result"][pair] = {"a": [a, "1000", "1000.000"], "b": [b, "2000", "2000.000"], "c": [c, "10.00000000"],
                          "v": [str(round(float(v) * 0.7, 8)), v], "p": [c, c], "t": [1000, 2500], "l": [b, b], "h": [a, a], "o": c}
write("kraken_ticker.json", kr)
COINBASE = {
    "USDT-USD": ("0.99920", "0.99921", "18000000.00"),
    "USDT-USDC": ("0.9993", "0.9994", "2100000.00"),
    "EURC-USDC": ("1.0851", "1.0852", "4800000"),
}
cb = {}
for i, (pid, (b, a, v)) in enumerate(COINBASE.items()):
    cb[pid] = {"http_status": 200, "body": {"ask": a, "bid": b, "volume": v, "trade_id": 100000 + i, "price": a, "size": "50.00",
                                             "time": f"2026-10-19T13:20:3{i}.596866968Z"}}
cb["USDT-USD"]["body"]["rfq_volume"] = "11000.000000"
cb["USDT-USDC"]["body"]["rfq_volume"] = "2400000.000000"
cb["USDC-USD"] = {"http_status": 404, "body": {"message": "NotFound"}}
write("coinbase_tickers.json", cb)


def bst(o, h, lo, last, vol, bid, ask, side="0", extra=None):
    b = {"timestamp": "1792416049", "open": o, "high": h, "low": lo, "last": last, "volume": vol, "vwap": last,
         "bid": bid, "ask": ask, "side": side, "open_24": o, "percent_change_24": "0.01", "market_type": "SPOT"}
    if extra:
        b.update(extra)
    return {"http_status": 200, "body": b}


bitstamp = {
    "usdtusd": bst("0.99915", "0.99930", "0.99905", "0.99920", "16000000.50000", "0.99919", "0.99920"),
    "usdcusd": bst("0.99988", "0.99993", "0.99980", "0.99987", "520000.25000", "0.99986", "0.99988"),
    "eurceur": bst("0.9990", "1.0000", "0.9950", "0.9981", "11554.14", "0.9987", "0.9999", side="1"),
    "eurusd": bst("1.08440", "1.08520", "1.08400", "1.08480", "1600000.50000", "1.08474", "1.08486", extra={"index_price": "1.08480"}),
    # An unknown pair answers 200 with the full ticker list (276 entries upstream; 3 kept).
    "eurcusd_unknown_pair": {"http_status": 200, "body_type": "list", "body_len": 276, "body": [
        dict(bst("50000.00", "51000.00", "49000.00", "50500.00", "1000.5", "50499.99", "50500.00")["body"], pair="BTC/USD", market="BTC/USD"),
        dict(bst("46000.00", "47000.00", "45000.00", "46500.00", "80.5", "46499.00", "46501.00")["body"], pair="BTC/EUR", market="BTC/EUR"),
        dict(bst("40000.00", "41000.00", "39000.00", "40500.00", "20.5", "40499.00", "40501.00")["body"], pair="BTC/GBP", market="BTC/GBP"),
    ]},
}
write("bitstamp_tickers.json", bitstamp)
write("okx_ticker.json", {"http_status": 200, "body": {"code": "0", "data": [{
    "instType": "SPOT", "instId": "USDC-USDT", "last": "1.00063", "lastSz": "90.5", "askPx": "1.00063", "askSz": "10000.5",
    "bidPx": "1.00062", "bidSz": "1400000.25", "open24h": "1.0009", "high24h": "1.00095", "low24h": "1.0006",
    "volCcy24h": "290000000.123456", "vol24h": "289800000.5", "ts": "1792416051080", "sodUtc0": "1.0008", "sodUtc8": "1.00085"}],
    "msg": ""}})
write("binance_ticker24h.json", {"http_status": 200, "body": {
    "symbol": "USDCUSDT", "priceChange": "-0.00020000", "priceChangePercent": "-0.020", "weightedAvgPrice": "1.00070000",
    "prevClosePrice": "1.00083000", "lastPrice": "1.00063000", "lastQty": "700.00000000", "bidPrice": "1.00062000",
    "bidQty": "3000000.00000000", "askPrice": "1.00063000", "askQty": "4500000.00000000", "openPrice": "1.00083000",
    "highPrice": "1.00090000", "lowPrice": "1.00060000", "volume": "3100000000.00000000", "quoteVolume": "3102170000.50000000",
    "openTime": 1792329652284, "closeTime": 1792416052284, "firstId": 1, "lastId": 600000, "count": 600000}})
for pair, (b, a, c, v) in KRAKEN.items():
    const(f"fxKraken{pair}Bid", float(b))
    const(f"fxKraken{pair}Ask", float(a))
    const(f"fxKraken{pair}Vol", float(v))
for pid, (b, a, v) in COINBASE.items():
    k = pid.replace("-", "")
    const(f"fxCoinbase{k}Bid", float(b))
    const(f"fxCoinbase{k}Ask", float(a))
    const(f"fxCoinbase{k}Vol", float(v))
for pair in ("usdtusd", "usdcusd", "eurceur", "eurusd"):
    body = bitstamp[pair]["body"]
    const(f"fxBitstamp{pair.upper()}Bid", float(body["bid"]))
    const(f"fxBitstamp{pair.upper()}Ask", float(body["ask"]))
    const(f"fxBitstamp{pair.upper()}Vol", float(body["volume"]))
const("fxOKXUSDCUSDTBid", 1.00062)
const("fxOKXUSDCUSDTAsk", 1.00063)
const("fxOKXUSDCUSDTVolQuote", 290000000.123456)
const("fxBinanceUSDCUSDTBid", 1.00062)
const("fxBinanceUSDCUSDTAsk", 1.00063)
const("fxBinanceUSDCUSDTVolQuote", 3102170000.5)

# ---------------------------------------------------------------- Uniswap v3 (eth_call)

UNI_PRICE = 1.00065  # USDT per USDC (token1/token0, both 6 decimals)
sqrt_x96 = int(Decimal(UNI_PRICE).sqrt() * (Decimal(2) ** 96))
pool = "0x3416cf6c708da44db2624d63ea0aaef7113527c6"


def word(n):
    return format(n & ((1 << 256) - 1), "064x")


tick = int(math.floor(math.log(UNI_PRICE) / math.log(1.0001)))
# slot0() returns 7 words: sqrtPriceX96, tick, observationIndex, observationCardinality,
# observationCardinalityNext, feeProtocol, unlocked.
slot0 = "0x" + word(sqrt_x96) + word(tick) + word(107) + word(300) + word(300) + word(68) + word(1)
assert len(slot0) == 2 + 7 * 64
write("uniswap_v3_usdc_usdt_rpc.json", [
    {"request": {"to": pool, "data": "0x0dfe1681"}, "response": {"jsonrpc": "2.0", "id": 1, "result": "0x" + word(0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48)}},
    {"request": {"to": pool, "data": "0xd21220a7"}, "response": {"jsonrpc": "2.0", "id": 1, "result": "0x" + word(0xdac17f958d2ee523a2206206994597c13d831ec7)}},
    {"request": {"to": pool, "data": "0xddca3f43"}, "response": {"jsonrpc": "2.0", "id": 1, "result": "0x" + word(100)}},
    {"request": {"to": pool, "data": "0x3850c7bd"}, "response": {"jsonrpc": "2.0", "id": 1, "result": slot0}},
])
const("fxUniswapSqrtPriceX96", str(sqrt_x96), "decimal string")
const("fxUniswapPrice", float((Decimal(sqrt_x96) / (Decimal(2) ** 96)) ** 2))
const("fxUniswapTick", tick)

# ---------------------------------------------------------------- Chainlink (v1.1)


def round_data(answer, updated):
    rid = (4 << 64) | 37
    return "0x" + word(rid) + word(answer) + word(updated - 22) + word(updated) + word(rid)


CL = [("0x8fFfFfd4AfB6115b954Bd326cbe7B4BA576818f6", "USDC / USD", 99_987_000), ("0x3E7d1eAB13ad0104d2750B8863b489D65364e32D", "USDT / USD", 99_921_000),
      ("0xb49f677943BC038e9857d61E7d053CaA2C1734C1", "EUR / USD", 108_476_000)]
cl = []
for addr, desc, ans in CL:
    enc = desc.encode()
    desc_hex = "0x" + word(32) + word(len(enc)) + enc.hex().ljust(64, "0")
    cl += [{"request": {"to": addr, "data": "0xfeaf968c"}, "response": {"jsonrpc": "2.0", "id": 1, "result": round_data(ans, 1792416000)}},
           {"request": {"to": addr, "data": "0x313ce567"}, "response": {"jsonrpc": "2.0", "id": 1, "result": "0x" + word(8)}},
           {"request": {"to": addr, "data": "0x7284e416"}, "response": {"jsonrpc": "2.0", "id": 1, "result": desc_hex}}]
write("chainlink_feeds_rpc.json", cl)

# ---------------------------------------------------------------- Curve 3pool (v1.1)

write("curve_3pool.json", {"success": True, "data": {"poolData": [{
    "id": "0", "address": "0xbEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7",
    "coinsAddresses": ["0x6B175474E89094C44Da98b954EedeAC495271d0F", "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
                       "0xdAC17F958D2ee523a2206206994597C13D831ec7", "0x0000000000000000000000000000000000000000"],
    "decimals": ["18", "6", "6", "0"], "virtualPrice": "1040000000000000000", "amplificationCoefficient": "4000",
    "totalSupply": "150000000000000000000000000", "name": "Curve.fi DAI/USDC/USDT", "assetType": "0",
    "lpTokenAddress": "0x6c3F90f043a72FA612cbac8115EE7e52BDe6E490", "priceOracle": None, "priceOracles": None, "symbol": "3Crv",
    "implementation": "", "assetTypeName": "usd",
    "coins": [
        {"address": "0x6B175474E89094C44Da98b954EedeAC495271d0F", "usdPrice": 0.99996, "decimals": "18", "isBasePoolLpToken": False,
         "symbol": "DAI", "name": "Dai Stablecoin", "poolBalance": "22000000000000000000000000"},
        {"address": "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", "usdPrice": 1, "decimals": "6", "isBasePoolLpToken": False,
         "symbol": "USDC", "name": "USD Coin", "poolBalance": "23000000000000"},
        {"address": "0xdAC17F958D2ee523a2206206994597C13D831ec7", "usdPrice": 1, "decimals": "6", "isBasePoolLpToken": False,
         "symbol": "USDT", "name": "Tether USD", "poolBalance": "105000000000000"}],
    "poolUrls": {"swap": ["https://example.invalid/curve/3pool", "https://example.invalid/curve/3pool/swap"],
                 "deposit": ["https://example.invalid/curve/3pool", "https://example.invalid/curve/3pool/deposit"],
                 "withdraw": ["https://example.invalid/curve/3pool", "https://example.invalid/curve/3pool/withdraw"]},
    "usdTotal": 150000000.0, "isMetaPool": False, "usdTotalExcludingBasePool": 150000000.0, "gaugeAddress": "",
    "gaugeRewards": [], "gaugeCrvApy": [1.5e-05, 3.75e-05], "gaugeFutureCrvApy": [1.4e-05, 3.5e-05], "usesRateOracle": False,
    "isBroken": False, "hasMethods": {"exchange_received": False, "exchange_extended": False},
    "creationTs": 1599400000, "creationBlockNumber": 10800000}],
    "tvlAll": 150000000.0, "tvl": 150000000.0}, "generatedTimeMs": 1792416051000})

# ---------------------------------------------------------------- Blockscout holders (v1.1)

def bs_tag(name, slug, ordinal=10, tag_type="name", meta=None):
    return {"meta": meta or {}, "name": name, "ordinal": ordinal, "slug": slug, "tagType": tag_type}


def bs_address(i, contract, tags, impl=None, name=None, proxy=None):
    return {"ens_domain_name": None, "hash": "0x" + format(0xA11CE0000000000000000000000000000000000 + i, "040x"),
            "implementations": [{"address_hash": "0x" + format(0xB0B0000000000000000000000000000000000000 + i, "040x"), "name": impl}]
            if impl else [],
            "is_contract": contract, "is_scam": False, "is_verified": contract,
            "metadata": {"tags": tags} if tags is not None else None,
            "name": name, "private_tags": [], "proxy_type": proxy, "public_tags": [], "reputation": "ok", "watchlist_names": []}


# Upstream quirks kept: a tag meta key with a trailing space ("tagIcon "), and
# metadata that is null for some holders.
holder_addrs = [
    bs_address(0, False, [
        bs_tag("Example: Vault", "example-vault", meta={"main_entity": "Example", "tooltipUrl": "https://example.invalid/vault"}),
        bs_tag("Example Vault Pocket", "example-vault-pocket", ordinal=0,
               meta={"tagIcon ": "https://example.invalid/tag.svg", "textColor": "#123456",
                     "tooltipAttribution": "https://example.invalid/attribution", "tooltipAttributionIcon": "https://example.invalid/tag.svg",
                     "tooltipDescription": "Synthetic label"}),
        bs_tag("Example", "example", ordinal=0, tag_type="protocol")]),
    bs_address(1, True, [bs_tag("Example Exchange Wallet 1", "example-exchange-wallet-1")], impl="ExampleWallet", name="ExampleProxy", proxy="eip1967"),
    bs_address(2, True, None, impl="ExampleWallet", name="ExampleProxy", proxy="eip1967"),
    bs_address(3, True, [bs_tag("Example Exchange Wallet 2", "example-exchange-wallet-2")], impl="ExampleWallet", name="ExampleProxy", proxy="eip1967"),
    bs_address(4, True, [bs_tag("Example Bridge", "example-bridge", meta={"tooltipUrl": "https://example.invalid/bridge"}),
                         bs_tag("Bridge", "bridge", ordinal=0, tag_type="generic"),
                         bs_tag("Example", "example", ordinal=0, tag_type="protocol")],
               impl="ExamplePredicate", name="ExampleManagerProxy", proxy="basic_implementation"),
]
holders = [{"address": a, "token_id": None, "value": str((5 - i) * 1_000_000_000_000_000)} for i, a in enumerate(holder_addrs)]
cloudflare = ('<!DOCTYPE html><html lang="en-US"><head><title>Just a moment...</title>'
              '<meta http-equiv="Content-Type" content="text/html; charset=UTF-8"></head><body>synthetic challenge page</body></html>')
write("blockscout_usdc_holders.json", {
    "eth": {"holders": {"http_status": 200, "body": {"items": holders, "next_page_params": {"value": holders[-1]["value"],
                                                                                              "address_hash": holders[-1]["address"]["hash"],
                                                                                              "items_count": 50}}},
            "token": {"http_status": 200, "body": {"address_hash": "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", "circulating_market_cap": "40000000000.0",
                                                   "circulating_supply": None, "decimals": "6", "exchange_rate": "1.0", "holders_count": "8000000",
                                                   "icon_url": "https://example.invalid/usdc.svg", "name": "USDC", "reputation": "ok", "symbol": "USDC",
                                                   "total_supply": "40000000000000000", "type": "ERC-20", "volume_24h": "5000000000.0"}}},
    "base": {"holders": {"http_status": 403, "body": cloudflare}, "token": {"http_status": 403, "body": cloudflare}},
})

# ---------------------------------------------------------------- replay scenario series


def chart_rows(day_values, peg="peggedUSD", usd_ratio=0.9998, unreleased=None):
    # Settled days carry integer amounts upstream.
    rows = []
    for d, v in day_values:
        r = {"date": str(unix(d)), "totalCirculating": {peg: v}}
        if unreleased is not None:
            r["totalUnreleased"] = {peg: unreleased}
        r["totalCirculatingUSD"] = {peg: int(round(v * usd_ratio))}
        rows.append(r)
    return rows


def path(first, last, start, factors):
    """Daily integer supply series: start × the day's factor (default +0.04%)."""
    ds = days(first, last)
    vals = []
    v = start
    for d in ds:
        v = v * factors.get(d, 1.0004)
        vals.append((d, int(round(v))))
    return vals


def flows(vals):
    one = [(vals[i][0], (vals[i][1] - vals[i - 1][1]) / vals[i - 1][1] * 100) for i in range(1, len(vals))]
    seven = [(vals[i][0], (vals[i][1] - vals[i - 7][1]) / vals[i - 7][1] * 100) for i in range(7, len(vals))]
    return min(one, key=lambda x: x[1]), min(seven, key=lambda x: x[1]), max(one, key=lambda x: x[1])


facts = {}
# USDT 2022-05 redemption wave: worst 24h −3.876% on 05-12, worst 7d −10.225% on 05-18.
f12 = 1 - 0.038760627375
rest = (1 - 0.10225371872) / f12
f13, f14, f15, f16, f17 = 0.98, 0.985, 0.99, 0.992, 0.994
f18 = rest / (f13 * f14 * f15 * f16 * f17)
usdt22 = path("2022-04-28", "2022-06-15", 70_000_000_000, {"2022-05-10": 0.997, "2022-05-11": 0.995, "2022-05-12": f12, "2022-05-13": f13,
                                                            "2022-05-14": f14, "2022-05-15": f15, "2022-05-16": f16, "2022-05-17": f17,
                                                            "2022-05-18": f18, "2022-05-19": 0.998, "2022-05-20": 0.999})
w1, w7, _ = flows(usdt22)
assert w1[0] == "2022-05-12" and abs(w1[1] + 3.8760627375) < 1e-6, w1
assert w7[0] == "2022-05-18" and abs(w7[1] + 10.225371872) < 1e-6, w7
write("replay/charts_usdt_2022-05.json", chart_rows(usdt22, unreleased=4_000_000_000))
facts["usdt_2022"] = (w1, w7)
# USDC 2023-03 (SVB): worst 24h −3.647% on 03-12, worst 7d −15.172% on 03-17.
g12 = 1 - 0.03647279921263
g11, g13, g14, g15, g16 = 0.985, 0.97, 0.972, 0.975, 0.978
g17 = (1 - 0.15172151836780) / (g11 * g12 * g13 * g14 * g15 * g16)
usdc23 = path("2023-03-01", "2023-03-31", 42_000_000_000, {"2023-03-10": 1.0, "2023-03-11": g11, "2023-03-12": g12, "2023-03-13": g13,
                                                            "2023-03-14": g14, "2023-03-15": g15, "2023-03-16": g16, "2023-03-17": g17,
                                                            "2023-03-18": 1.001})
w1, w7, _ = flows(usdc23)
assert w1[0] == "2023-03-12" and abs(w1[1] + 3.647279921263) < 1e-6, w1
assert w7[0] == "2023-03-17" and abs(w7[1] + 15.17215183678) < 1e-6, w7
write("replay/charts_usdc_2023-03.json", chart_rows(usdc23, unreleased=4_400_000_000))
facts["usdc_2023"] = (w1, w7)
# 2024 supply windows (measured: USDT −1.379% on 2024-12-01, not reversed;
# USDC −6.483% on 2024-04-18, +8.591% the next day, and more, below).
# USDT 2024-11/12: the measured 24h changes (%, 0.001% precision) on a made-up
# level; 12-01 and the 7d change on 12-31 are exact. The 12-01 drop is not
# reversed (+0.063% on 12-02), so S4's 3-point median keeps it (M4 replay).
usdt24_pct = {
    "2024-11-21": 1.002, "2024-11-22": 0.240, "2024-11-23": 0.999, "2024-11-24": 0.858, "2024-11-25": 0.307,
    "2024-11-26": 0.200, "2024-11-27": 0.022, "2024-11-28": 0.308, "2024-11-29": 0.613, "2024-11-30": 0.225,
    "2024-12-02": 0.063, "2024-12-03": 0.398, "2024-12-04": 0.241, "2024-12-05": 0.523, "2024-12-06": 0.683,
    "2024-12-07": 0.776, "2024-12-08": 0.162, "2024-12-09": 0.099, "2024-12-10": 0.176, "2024-12-11": 0.224,
    "2024-12-12": 0.757, "2024-12-13": 0.285, "2024-12-14": 0.031, "2024-12-15": 0.085, "2024-12-16": 0.000,
    "2024-12-17": 0.136, "2024-12-18": 0.109, "2024-12-19": 0.015, "2024-12-20": -0.033, "2024-12-21": -0.297,
    "2024-12-22": -0.207, "2024-12-23": -0.018, "2024-12-24": -0.251, "2024-12-25": -0.052, "2024-12-26": 0.003,
    "2024-12-27": 0.010, "2024-12-28": -0.109, "2024-12-29": -0.204, "2024-12-30": -0.037,
}
f1231 = (1 - 0.01209584903571) / math.prod(1 + usdt24_pct[d] / 100 for d in days("2024-12-25", "2024-12-30"))
usdt24 = path("2024-11-20", "2024-12-31", 91_000_000_000,
              {**{d: 1 + p / 100 for d, p in usdt24_pct.items()}, "2024-12-01": 1 - 0.013790590833, "2024-12-31": f1231})
w1, w7, _ = flows(usdt24)
assert w1[0] == "2024-12-01" and abs(w1[1] + 1.3790590833) < 1e-6, w1
assert w7[0] == "2024-12-31" and abs(w7[1] + 1.209584903571) < 1e-6, w7
one = {usdt24[i][0]: (usdt24[i][1] - usdt24[i - 1][1]) / usdt24[i - 1][1] * 100 for i in range(1, len(usdt24))}
assert abs(one["2024-12-02"] - 0.063) < 5e-3 and abs(one["2024-12-31"] + 0.824) < 5e-3, (one["2024-12-02"], one["2024-12-31"])
write("replay/charts_usdt_2024.json", chart_rows(usdt24))
h18, h19 = 1 - 0.0648292120695, 1.08591249303842
# USDC 2024-04: the aggregator chart dips and rebounds over five days, after a
# real outflow of about 3% a week. Measured 24h changes (%, 0.001% precision,
# 04-18 and 04-19 exact) on a made-up level; S4's quiet case (stablecoin.md §13)
# depends on this shape: 04-20's −4.450% is followed by a 7d change of −2.045%.
usdc24_pct = {
    "2024-04-02": 0.629, "2024-04-03": 0.545, "2024-04-04": 0.093, "2024-04-05": 0.439, "2024-04-06": -0.329,
    "2024-04-07": 0.077, "2024-04-08": -0.062, "2024-04-09": -0.959, "2024-04-10": -0.969, "2024-04-11": -0.312,
    "2024-04-12": 0.100, "2024-04-13": -0.904, "2024-04-14": 1.242, "2024-04-15": -0.183, "2024-04-16": 0.350,
    "2024-04-17": 0.614, "2024-04-20": -4.450, "2024-04-21": 0.169, "2024-04-22": 7.284, "2024-04-23": -0.451,
    "2024-04-24": -1.020, "2024-04-25": 0.0, "2024-04-26": 0.0,
}
usdc24 = path("2024-04-01", "2024-04-26", 28_000_000_000,
              {**{d: 1 + p / 100 for d, p in usdc24_pct.items()}, "2024-04-18": h18, "2024-04-19": h19})
w1, w7, b1 = flows(usdc24)
assert w1[0] == "2024-04-18" and abs(w1[1] + 6.48292120695) < 1e-6, w1
assert w7[0] == "2024-04-18" and abs(w7[1] + 5.35031928257) < 5e-3, w7
assert b1[0] == "2024-04-19" and abs(b1[1] - 8.591249303842) < 1e-6, b1
one = {usdc24[i][0]: (usdc24[i][1] - usdc24[i - 1][1]) / usdc24[i - 1][1] * 100 for i in range(1, len(usdc24))}
seven = {usdc24[i][0]: (usdc24[i][1] - usdc24[i - 7][1]) / usdc24[i - 7][1] * 100 for i in range(7, len(usdc24))}
for d, (want1, want7) in {"2024-04-11": (-0.312, -2.104), "2024-04-12": (0.100, -2.435), "2024-04-13": (-0.904, -2.998),
                          "2024-04-14": (1.242, -1.868), "2024-04-17": (0.614, 0.895), "2024-04-18": (-6.483, -5.350),
                          "2024-04-19": (8.591, 2.679), "2024-04-20": (-4.450, -0.996), "2024-04-21": (0.169, -2.045),
                          "2024-04-22": (7.284, 5.284)}.items():
    assert abs(one[d] - want1) < 5e-3 and abs(seven[d] - want7) < 5e-3, (d, one[d], seven[d])
write("replay/charts_usdc_2024.json", chart_rows(usdc24))


def price_rows(series, coins):
    # Upstream writes an exact peg as the integer 1.
    out = []
    for d in sorted(series):
        out.append({"date": unix(d), "prices": {c: (1 if series[d][c] == 1.0 else series[d][c]) for c in coins if c in series[d]}})
    return out


# DefiLlama daily prices, 2022-04-28..06-15: the May depeg is invisible (worst
# discount in 05-09..05-16 is −10.54 bps on 05-15); a +32.7 bps premium on 05-01
# and −17 bps on 06-14 are the window's extremes.
p22 = {}
for i, d in enumerate(days("2022-04-28", "2022-06-15")):
    p22[d] = {"tether": round(1 + ((i % 5) - 2) * 0.0001, 6), "usd-coin": round(1 + ((i % 3) - 1) * 0.0002, 6)}
for d in days("2022-05-09", "2022-05-14"):
    p22[d]["tether"] = 1.0
p22["2022-05-01"]["tether"] = 1.00326891680293
p22["2022-05-15"]["tether"] = 0.998946
p22["2022-05-16"]["tether"] = 0.999118
p22["2022-06-14"]["tether"] = 0.9983
p22["2022-05-07"]["usd-coin"] = 0.99703949025748
write("replay/prices_2022-05.json", price_rows(p22, ["tether", "usd-coin"]))
# 2023-03: USDC 0.99755917 on 03-11 and 0.961065 on 03-12; USDT trades at a premium.
p23 = {}
for i, d in enumerate(days("2023-03-01", "2023-03-31")):
    p23[d] = {"tether": round(1 + ((i % 3) - 1) * 0.0001, 6), "usd-coin": round(1 - ((i % 4) * 0.00005), 6), "euro-coin": round(1.06 + (i % 7) * 0.001, 6)}
for d, v in {"2023-03-13": 1.01, "2023-03-14": 1.004, "2023-03-16": 1.006, "2023-03-17": 1.006, "2023-03-18": 1.005, "2023-03-19": 1.008,
             "2023-03-20": 1.007, "2023-03-21": 1.011, "2023-03-22": 1.006, "2023-03-23": 1.006, "2023-03-24": 1.005, "2023-03-25": 1.007,
             "2023-03-26": 1.008}.items():
    p23[d]["tether"] = v
for d, v in {"2023-03-11": 0.99755917, "2023-03-12": 0.961065, "2023-03-13": 0.991923, "2023-03-17": 1.004, "2023-03-19": 1.005,
             "2023-03-20": 1.004, "2023-03-21": 1.006, "2023-03-23": 1.004, "2023-03-25": 1.006, "2023-03-26": 1.006}.items():
    p23[d]["usd-coin"] = v
# Days at exactly 30.0 bps (price 1.003), as upstream has them: 1.003 − 1 is
# 29.99999999999989 bps in float64, so a naive ">= 30" comparison misses them.
for d in ("2023-03-11", "2023-03-31"):
    p23[d]["tether"] = 1.003
p23["2023-03-18"]["usd-coin"] = 1.003
write("replay/prices_2023-03.json", price_rows(p23, ["tether", "usd-coin", "euro-coin"]))
# 2024 (sparse): every day at ≥ 30 bps from the measured year plus quiet days;
# USDT 10 days (max 60 bps), USDC 14 days (max 80 bps), none at ≥ 100 bps.
USDT24 = {"2024-02-15": 1.004, "2024-08-08": 1.006, "2024-10-07": 0.99665, "2024-10-25": 0.995342, "2024-11-11": 1.005}
USDC24 = {"2024-01-10": 0.99696, "2024-02-15": 1.005, "2024-03-05": 0.99695, "2024-03-07": 0.99652, "2024-03-20": 1.006,
          "2024-08-08": 1.005, "2024-10-25": 0.996137, "2024-11-11": 1.008, "2024-11-26": 0.99685}
# Plus the measured days at exactly 30.0 bps (1.003); see the 2023 note above.
USDT24.update({d: 1.003 for d in ("2024-01-02", "2024-03-20", "2024-07-13", "2024-07-20", "2024-10-06")})
USDC24.update({d: 1.003 for d in ("2024-01-02", "2024-03-01", "2024-03-15", "2024-07-13", "2024-11-25")})
p24 = {}
for m in range(1, 13):
    for dd in (1, 15):
        p24[f"2024-{m:02d}-{dd:02d}"] = {}
for d in set(USDT24) | set(USDC24):
    p24[d] = {}
for i, d in enumerate(sorted(p24)):
    p24[d]["tether"] = USDT24.get(d, round(1 + ((i % 5) - 2) * 0.0003, 6))
    p24[d]["usd-coin"] = USDC24.get(d, round(1 - ((i % 4) - 1) * 0.0004, 6))
write("replay/prices_2024.json", price_rows(p24, ["tether", "usd-coin"]))


def count_ge(rows, coin, bps, naive=False):
    """Days with |price − 1| ≥ bps. naive=True compares raw float64 bps, which
    reads 1.003 as 29.99999999999989 (the way the design doc's counts were made)."""
    eps = 0 if naive else 1e-9
    return sum(1 for r in rows if coin in r["prices"] and abs(r["prices"][coin] - 1) * 1e4 >= bps - eps)


r24 = price_rows(p24, ["tether", "usd-coin"])
assert count_ge(r24, "tether", 30) == 10 and count_ge(r24, "usd-coin", 30) == 14, (count_ge(r24, "tether", 30), count_ge(r24, "usd-coin", 30))
assert count_ge(r24, "tether", 30, naive=True) == 5 and count_ge(r24, "usd-coin", 30, naive=True) == 9
assert count_ge(r24, "tether", 100) == 0 and count_ge(r24, "usd-coin", 100) == 0
assert abs(max(abs(r["prices"]["tether"] - 1) for r in r24) * 1e4 - 60) < 1e-6
assert abs(max(abs(r["prices"]["usd-coin"] - 1) for r in r24) * 1e4 - 80) < 1e-6
r22 = price_rows(p22, ["tether", "usd-coin"])
assert count_ge(r22, "tether", 30) == 1 and count_ge(r22, "usd-coin", 30) == 0
window = [(r["date"], (r["prices"]["tether"] - 1) * 1e4) for r in r22 if unix("2022-05-09") <= r["date"] <= unix("2022-05-16")]
worst = min(window, key=lambda x: x[1])
assert worst[0] == unix("2022-05-15") and abs(worst[1] + 10.54) < 1e-6, worst
r23 = price_rows(p23, ["tether", "usd-coin", "euro-coin"])
assert count_ge(r23, "tether", 30) == 15 and count_ge(r23, "tether", 100) == 2
assert count_ge(r23, "usd-coin", 30) == 10 and count_ge(r23, "usd-coin", 100) == 1
assert count_ge(r23, "tether", 30, naive=True) == 13 and count_ge(r23, "usd-coin", 30, naive=True) == 9


def ohlc(pair, first, special_day, special, base):
    rows = []
    for i, d in enumerate(days(first, (dt.date.fromisoformat(first) + dt.timedelta(days=7)).isoformat())):
        if d == special_day:
            o, h, lo, c = special
        else:
            o, h, lo, c = base[i]
        rows.append({"timestamp": str(unix(d)), "open": o, "high": h, "low": lo, "close": c, "volume": f"{1_000_000 + i * 250_000}.5"})
    return {"data": {"pair": pair, "ohlc": rows}}


write("replay/bitstamp_ohlc_usdcusd_2023-03.json", ohlc("USDC/USD", "2023-03-08", "2023-03-11", ("0.99729", "0.99975", "0.80526", "0.96853"),
                                                       [("0.99990", "0.99995", "0.99770", "0.99988"), ("0.99988", "0.99996", "0.99690", "0.99992"),
                                                        ("0.99992", "1.00002", "0.99100", "0.99780"), None, ("0.96720", "0.99420", "0.94470", "0.99150"),
                                                        ("0.99190", "0.99950", "0.95000", "0.99550"), ("0.99550", "0.99950", "0.99300", "0.99800"),
                                                        ("0.99800", "0.99990", "0.99600", "0.99900")]))
write("replay/bitstamp_ohlc_usdtusd_2022-05.json", ohlc("USDT/USD", "2022-05-09", "2022-05-12", ("0.99523", "0.99939", "0.91011", "0.99675"),
                                                       [("0.99990", "1.00010", "0.99800", "0.99950"), ("0.99950", "1.00000", "0.99300", "0.99700"),
                                                        ("0.99700", "0.99900", "0.98500", "0.99520"), None, ("0.99680", "0.99950", "0.98900", "0.99800"),
                                                        ("0.99800", "1.00000", "0.99500", "0.99900"), ("0.99900", "1.00010", "0.99700", "0.99950"),
                                                        ("0.99950", "1.00010", "0.99800", "0.99960")]))

# ---------------------------------------------------------------- consistency

# Aggregator totals stay within S12's 0.5% watch level of the issuer totals.
for sym, issuer in (("USDT", liab), ("USDC", usdc_total), ("EURC", eurc_total)):
    diff = (Decimal(str(LLAMA[sym]["cur"])) - issuer) / issuer * 100
    assert abs(diff) < Decimal("0.5"), (sym, diff)

# ---------------------------------------------------------------- Go constants

with open(GO_OUT, "w") as f:
    f.write("// Code generated by testdata/stablecoin/generate.py; DO NOT EDIT.\n\n")
    f.write("package main\n\n")
    f.write("// Values of the synthetic fixtures in testdata/stablecoin (all made up; see\n")
    f.write("// testdata/stablecoin/README.md). Tests assert against these names.\n")
    f.write("const (\n")
    for name, lit, comment in CONSTS:
        f.write(f"\t{name} = {lit}")
        if comment:
            f.write(f" // {comment}")
        f.write("\n")
    f.write(")\n")
try:
    subprocess.run(["gofmt", "-w", GO_OUT], check=True)
except (OSError, subprocess.CalledProcessError) as e:
    print("warning: gofmt failed on", GO_OUT, e)
print("wrote fixtures and", os.path.relpath(GO_OUT, HERE))
for k, v in facts.items():
    print(k, v)
