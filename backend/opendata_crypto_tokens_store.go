package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const tokenEconomySchema = `
CREATE TABLE IF NOT EXISTS token_economy_daily (
  day                  TEXT PRIMARY KEY,
  transfers            INTEGER NOT NULL DEFAULT 0,
  zero_value_transfers INTEGER NOT NULL DEFAULT 0,
  native_txs           INTEGER NOT NULL DEFAULT 0,
  contracts            INTEGER NOT NULL DEFAULT 0,
  erc20                INTEGER NOT NULL DEFAULT 0,
  erc721               INTEGER NOT NULL DEFAULT 0,
  synced_at            TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS token_major_daily (
  day            TEXT NOT NULL,
  token_address  TEXT NOT NULL,
  transfers      INTEGER NOT NULL DEFAULT 0,
  zero_value     INTEGER NOT NULL DEFAULT 0,
  senders        INTEGER NOT NULL DEFAULT 0,
  receivers      INTEGER NOT NULL DEFAULT 0,
  volume_token   REAL NOT NULL DEFAULT 0,
  minted_token   REAL NOT NULL DEFAULT 0,
  burned_token   REAL NOT NULL DEFAULT 0,
  synced_at      TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (day, token_address)
);

CREATE TABLE IF NOT EXISTS token_contract_metadata (
  address         TEXT PRIMARY KEY,
  symbol          TEXT NOT NULL DEFAULT '',
  name            TEXT NOT NULL DEFAULT '',
  token_standard  TEXT NOT NULL DEFAULT '',
  decimals        INTEGER NOT NULL DEFAULT 0,
  proxy_standard  TEXT NOT NULL DEFAULT '',
  creator_address TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL DEFAULT '',
  found           INTEGER NOT NULL DEFAULT 1,
  updated_at      TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS token_supply_cache (
  asset         TEXT NOT NULL,
  network       TEXT NOT NULL,
  network_label TEXT NOT NULL DEFAULT '',
  contract      TEXT NOT NULL,
  tier          TEXT NOT NULL,
  label         TEXT NOT NULL,
  supply        REAL NOT NULL DEFAULT 0,
  updated_at    TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (asset, network, contract)
);

CREATE TABLE IF NOT EXISTS token_market_cache (
  address        TEXT PRIMARY KEY,
  symbol         TEXT NOT NULL DEFAULT '',
  price_usd      REAL NOT NULL DEFAULT 0,
  holders_count  INTEGER NOT NULL DEFAULT 0,
  volume_24h_usd REAL NOT NULL DEFAULT 0,
  market_cap_usd REAL NOT NULL DEFAULT 0,
  updated_at     TEXT NOT NULL DEFAULT ''
);`

type tokenEconomyDailyRow struct {
	Day                string
	Transfers          int64
	ZeroValueTransfers int64
	NativeTxs          int64
	Contracts          int64
	ERC20              int64
	ERC721             int64
}

type tokenMajorDailyRow struct {
	Day          string
	TokenAddress string
	Transfers    int64
	ZeroValue    int64
	Senders      int64
	Receivers    int64
	VolumeToken  float64
	MintedToken  float64
	BurnedToken  float64
}

type tokenContractMeta struct {
	Address        string
	Symbol         string
	Name           string
	TokenStandard  string
	Decimals       int
	ProxyStandard  string
	CreatorAddress string
	CreatedAt      string
	Found          bool
	UpdatedAt      string
}

type TokenSupplyRow struct {
	Asset        string  `json:"asset"`
	Network      string  `json:"network"`
	NetworkLabel string  `json:"network_label"`
	Contract     string  `json:"contract"`
	Tier         string  `json:"tier"`
	Label        string  `json:"label"`
	Supply       float64 `json:"supply"`
	UpdatedAt    string  `json:"updated_at"`
	Stale        bool    `json:"stale,omitempty"`
	Error        string  `json:"error,omitempty"`
}

type TokenMarketRow struct {
	Address      string  `json:"address"`
	Symbol       string  `json:"symbol"`
	PriceUSD     float64 `json:"price_usd"`
	HoldersCount int64   `json:"holders_count"`
	Volume24hUSD float64 `json:"volume_24h_usd"`
	MarketCapUSD float64 `json:"market_cap_usd"`
	UpdatedAt    string  `json:"updated_at"`
	Stale        bool    `json:"stale,omitempty"`
}

func (s *riskStore) upsertTokenEconomyDaily(ctx context.Context, rows []tokenEconomyDailyRow, syncedAt time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
INSERT OR IGNORE INTO token_economy_daily
  (day, transfers, zero_value_transfers, native_txs, contracts, erc20, erc721, synced_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	at := syncedAt.UTC().Format(time.RFC3339)
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.Day, r.Transfers, r.ZeroValueTransfers, r.NativeTxs, r.Contracts, r.ERC20, r.ERC721, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *riskStore) loadTokenEconomyDaily(ctx context.Context, startDate, endDate string) ([]tokenEconomyDailyRow, error) {
	rs, err := s.db.QueryContext(ctx, `
SELECT day, transfers, zero_value_transfers, native_txs, contracts, erc20, erc721
FROM token_economy_daily
WHERE day >= ? AND day < ?
ORDER BY day ASC`, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []tokenEconomyDailyRow
	for rs.Next() {
		var r tokenEconomyDailyRow
		if err := rs.Scan(&r.Day, &r.Transfers, &r.ZeroValueTransfers, &r.NativeTxs, &r.Contracts, &r.ERC20, &r.ERC721); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func (s *riskStore) upsertTokenMajorDaily(ctx context.Context, rows []tokenMajorDailyRow, syncedAt time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
INSERT OR IGNORE INTO token_major_daily
  (day, token_address, transfers, zero_value, senders, receivers, volume_token, minted_token, burned_token, synced_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	at := syncedAt.UTC().Format(time.RFC3339)
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.Day, strings.ToLower(r.TokenAddress), r.Transfers, r.ZeroValue, r.Senders, r.Receivers, r.VolumeToken, r.MintedToken, r.BurnedToken, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *riskStore) loadTokenMajorDaily(ctx context.Context, startDate, endDate string) ([]tokenMajorDailyRow, error) {
	rs, err := s.db.QueryContext(ctx, `
SELECT day, token_address, transfers, zero_value, senders, receivers, volume_token, minted_token, burned_token
FROM token_major_daily
WHERE day >= ? AND day < ?
ORDER BY day ASC, token_address ASC`, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []tokenMajorDailyRow
	for rs.Next() {
		var r tokenMajorDailyRow
		if err := rs.Scan(&r.Day, &r.TokenAddress, &r.Transfers, &r.ZeroValue, &r.Senders, &r.Receivers, &r.VolumeToken, &r.MintedToken, &r.BurnedToken); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// upsertTokenMetadataBatch writes metadata for foundRows and records a
// negative-cache row (found = 0) for any address in requestedAddrs that was
// not returned by tokens_latest, preventing repeated 1.70 GiB scans.
func (s *riskStore) upsertTokenMetadataBatch(ctx context.Context, requestedAddrs []string, foundRows []tokenContractMeta, now time.Time) error {
	if len(requestedAddrs) == 0 && len(foundRows) == 0 {
		return nil
	}
	foundByAddr := make(map[string]tokenContractMeta, len(foundRows))
	for _, r := range foundRows {
		addr := strings.ToLower(strings.TrimSpace(r.Address))
		if addr == "" {
			continue
		}
		r.Address = addr
		r.Found = true
		foundByAddr[addr] = r
	}
	for _, req := range requestedAddrs {
		addr := strings.ToLower(strings.TrimSpace(req))
		if addr == "" {
			continue
		}
		if _, ok := foundByAddr[addr]; !ok {
			foundByAddr[addr] = tokenContractMeta{Address: addr, Found: false}
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO token_contract_metadata
  (address, symbol, name, token_standard, decimals, proxy_standard, creator_address, created_at, found, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(address) DO UPDATE SET
  symbol = excluded.symbol,
  name = excluded.name,
  token_standard = excluded.token_standard,
  decimals = excluded.decimals,
  proxy_standard = excluded.proxy_standard,
  creator_address = excluded.creator_address,
  created_at = excluded.created_at,
  found = excluded.found,
  updated_at = excluded.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	at := now.UTC().Format(time.RFC3339)
	for _, r := range foundByAddr {
		foundInt := 0
		if r.Found {
			foundInt = 1
		}
		if _, err := stmt.ExecContext(ctx, r.Address, r.Symbol, r.Name, r.TokenStandard, r.Decimals, r.ProxyStandard, strings.ToLower(r.CreatorAddress), r.CreatedAt, foundInt, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *riskStore) loadTokenMetadata(ctx context.Context, addrs []string, now time.Time, maxAge time.Duration) (map[string]tokenContractMeta, error) {
	out := make(map[string]tokenContractMeta, len(addrs))
	if len(addrs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(addrs))
	args := make([]any, len(addrs))
	for i, a := range addrs {
		placeholders[i] = "?"
		args[i] = strings.ToLower(strings.TrimSpace(a))
	}
	query := fmt.Sprintf(`
SELECT address, symbol, name, token_standard, decimals, proxy_standard, creator_address, created_at, found, updated_at
FROM token_contract_metadata
WHERE address IN (%s)`, strings.Join(placeholders, ","))
	rs, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	cutoff := now.UTC().Add(-maxAge)
	for rs.Next() {
		var (
			r        tokenContractMeta
			foundInt int
		)
		if err := rs.Scan(&r.Address, &r.Symbol, &r.Name, &r.TokenStandard, &r.Decimals, &r.ProxyStandard, &r.CreatorAddress, &r.CreatedAt, &foundInt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Found = foundInt != 0
		if t, err := time.Parse(time.RFC3339, r.UpdatedAt); err == nil && !t.Before(cutoff) {
			out[r.Address] = r
		}
	}
	return out, rs.Err()
}

func (s *riskStore) upsertTokenSupplyCache(ctx context.Context, rows []TokenSupplyRow, now time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO token_supply_cache
  (asset, network, network_label, contract, tier, label, supply, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(asset, network, contract) DO UPDATE SET
  network_label = excluded.network_label,
  tier = excluded.tier,
  label = excluded.label,
  supply = excluded.supply,
  updated_at = excluded.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	at := now.UTC().Format(time.RFC3339)
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.Asset, r.Network, r.NetworkLabel, r.Contract, r.Tier, r.Label, r.Supply, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *riskStore) loadTokenSupplyCache(ctx context.Context) ([]TokenSupplyRow, time.Time, error) {
	rs, err := s.db.QueryContext(ctx, `
SELECT asset, network, network_label, contract, tier, label, supply, updated_at
FROM token_supply_cache
ORDER BY asset DESC, network ASC, tier ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rs.Close()
	var (
		out  []TokenSupplyRow
		asOf time.Time
	)
	for rs.Next() {
		var r TokenSupplyRow
		if err := rs.Scan(&r.Asset, &r.Network, &r.NetworkLabel, &r.Contract, &r.Tier, &r.Label, &r.Supply, &r.UpdatedAt); err != nil {
			return nil, time.Time{}, err
		}
		if t, err := time.Parse(time.RFC3339, r.UpdatedAt); err == nil && t.After(asOf) {
			asOf = t
		}
		out = append(out, r)
	}
	return out, asOf, rs.Err()
}

func (s *riskStore) upsertTokenMarketCache(ctx context.Context, rows []TokenMarketRow, now time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO token_market_cache
  (address, symbol, price_usd, holders_count, volume_24h_usd, market_cap_usd, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(address) DO UPDATE SET
  symbol = excluded.symbol,
  price_usd = excluded.price_usd,
  holders_count = excluded.holders_count,
  volume_24h_usd = excluded.volume_24h_usd,
  market_cap_usd = excluded.market_cap_usd,
  updated_at = excluded.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	at := now.UTC().Format(time.RFC3339)
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, strings.ToLower(r.Address), r.Symbol, r.PriceUSD, r.HoldersCount, r.Volume24hUSD, r.MarketCapUSD, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *riskStore) loadTokenMarketCache(ctx context.Context) ([]TokenMarketRow, time.Time, error) {
	rs, err := s.db.QueryContext(ctx, `
SELECT address, symbol, price_usd, holders_count, volume_24h_usd, market_cap_usd, updated_at
FROM token_market_cache
ORDER BY market_cap_usd DESC, symbol ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rs.Close()
	var (
		out  []TokenMarketRow
		asOf time.Time
	)
	for rs.Next() {
		var r TokenMarketRow
		if err := rs.Scan(&r.Address, &r.Symbol, &r.PriceUSD, &r.HoldersCount, &r.Volume24hUSD, &r.MarketCapUSD, &r.UpdatedAt); err != nil {
			return nil, time.Time{}, err
		}
		if t, err := time.Parse(time.RFC3339, r.UpdatedAt); err == nil && t.After(asOf) {
			asOf = t
		}
		out = append(out, r)
	}
	return out, asOf, rs.Err()
}
