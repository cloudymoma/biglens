package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	solanaSystemProgramID = "11111111111111111111111111111111"

	paySolRecentMaxTxs   = 15
	paySolHistoryMaxTxs  = 40
	paySolSigPageSize    = 50
	paySolTxConcurrency  = 6
	paySolFinalizedTxTTL = 10 * time.Minute
	paySolMintMetaTTL    = 1 * time.Hour
)

type solSigRow struct {
	Signature string `json:"signature"`
	Slot      uint64 `json:"slot"`
	BlockTime *int64 `json:"blockTime"`
	Err       any    `json:"err"`
}

type solAccountKey string

func (k *solAccountKey) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*k = solAccountKey(s)
		return nil
	}
	var obj struct {
		Pubkey string `json:"pubkey"`
	}
	if err := json.Unmarshal(data, &obj); err == nil {
		*k = solAccountKey(obj.Pubkey)
		return nil
	}
	return nil
}

type solInstruction struct {
	Program   string          `json:"program"`
	ProgramID string          `json:"programId"`
	Parsed    json.RawMessage `json:"parsed"`
}

type solTokenBalance struct {
	AccountIndex  int    `json:"accountIndex"`
	Mint          string `json:"mint"`
	Owner         string `json:"owner"`
	UITokenAmount struct {
		Amount   string `json:"amount"`
		Decimals int    `json:"decimals"`
	} `json:"uiTokenAmount"`
}

type solTxResult struct {
	Slot        uint64 `json:"slot"`
	BlockTime   *int64 `json:"blockTime"`
	Transaction struct {
		Message struct {
			AccountKeys  []solAccountKey  `json:"accountKeys"`
			Instructions []solInstruction `json:"instructions"`
		} `json:"message"`
	} `json:"transaction"`
	Meta *struct {
		Err               any               `json:"err"`
		Fee               uint64            `json:"fee"`
		PreBalances       []uint64          `json:"preBalances"`
		PostBalances      []uint64          `json:"postBalances"`
		PreTokenBalances  []solTokenBalance `json:"preTokenBalances"`
		PostTokenBalances []solTokenBalance `json:"postTokenBalances"`
		InnerInstructions []struct {
			Index        int              `json:"index"`
			Instructions []solInstruction `json:"instructions"`
		} `json:"innerInstructions"`
		LoadedAddresses *struct {
			Writable []string `json:"writable"`
			Readonly []string `json:"readonly"`
		} `json:"loadedAddresses"`
	} `json:"meta"`
}

func (tx *solTxResult) resolvedAccountKeys() []string {
	keys := make([]string, 0, len(tx.Transaction.Message.AccountKeys))
	for _, k := range tx.Transaction.Message.AccountKeys {
		keys = append(keys, string(k))
	}
	if tx.Meta != nil && tx.Meta.LoadedAddresses != nil {
		keys = append(keys, tx.Meta.LoadedAddresses.Writable...)
		keys = append(keys, tx.Meta.LoadedAddresses.Readonly...)
	}
	return keys
}

func (tx *solTxResult) flatInstructions() []solInstruction {
	top := tx.Transaction.Message.Instructions
	if tx.Meta == nil || len(tx.Meta.InnerInstructions) == 0 {
		return top
	}
	innerByIdx := make(map[int][]solInstruction, len(tx.Meta.InnerInstructions))
	for _, grp := range tx.Meta.InnerInstructions {
		innerByIdx[grp.Index] = append(innerByIdx[grp.Index], grp.Instructions...)
	}
	var out []solInstruction
	for i, ix := range top {
		out = append(out, ix)
		if inners := innerByIdx[i]; len(inners) > 0 {
			out = append(out, inners...)
		}
	}
	return out
}

func fetchSolanaSigsTarget(ctx context.Context, rpcs []string, target string, sinceUnix int64, maxKeep int, pageLimit int) ([]solSigRow, bool, string) {
	var kept []solSigRow
	var before string
	truncated := false

	for {
		cfg := map[string]any{
			"limit":      pageLimit,
			"commitment": "confirmed", // B1: never rely on default finalized commitment
		}
		if before != "" {
			cfg["before"] = before
		}
		res, code := solanaRPCFailover(ctx, rpcs, "getSignaturesForAddress", target, cfg)
		if code != "" {
			return nil, false, code
		}
		var page []solSigRow
		if err := json.Unmarshal(res, &page); err != nil {
			return nil, false, "bad_response"
		}
		if len(page) == 0 {
			break
		}

		reachedSince := false
		for _, r := range page {
			if sinceUnix > 0 && r.BlockTime != nil && *r.BlockTime < sinceUnix {
				reachedSince = true
				break
			}
			kept = append(kept, r)
		}

		if sinceUnix == 0 {
			// Single-page mode (/live)
			break
		}
		if reachedSince || len(page) < pageLimit {
			break
		}
		if len(kept) > maxKeep {
			truncated = true
			break
		}
		before = page[len(page)-1].Signature
	}

	if len(kept) > maxKeep {
		kept = kept[:maxKeep]
		truncated = true
	}
	return kept, truncated, ""
}

func collectSolanaSignatures(ctx context.Context, rpcs []string, asset, addr string, sinceUnix int64, maxKeep int, pageLimit int) ([]solSigRow, bool, error) {
	targets := []string{addr}
	if asset != "SOL" {
		for _, tok := range tokensFor(asset, "sol") {
			if ata, err := deriveSolanaATA(addr, tok.Contract); err == nil {
				targets = append([]string{ata}, targets...)
			}
		}
	}

	type targetRes struct {
		rows  []solSigRow
		trunc bool
		code  string
	}
	results := make([]targetRes, len(targets))
	var wg sync.WaitGroup
	for i, tgt := range targets {
		wg.Add(1)
		go func(i int, tgt string) {
			defer wg.Done()
			rows, trunc, code := fetchSolanaSigsTarget(ctx, rpcs, tgt, sinceUnix, maxKeep, pageLimit)
			results[i] = targetRes{rows: rows, trunc: trunc, code: code}
		}(i, tgt)
	}
	wg.Wait()

	for _, r := range results {
		if r.code != "" {
			return nil, false, errors.New(r.code)
		}
	}

	seen := make(map[string]solSigRow)
	truncated := false
	for _, r := range results {
		if r.trunc {
			truncated = true
		}
		for _, row := range r.rows {
			if existing, ok := seen[row.Signature]; !ok || row.Slot > existing.Slot {
				seen[row.Signature] = row
			}
		}
	}

	merged := make([]solSigRow, 0, len(seen))
	for _, row := range seen {
		merged = append(merged, row)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Slot != merged[j].Slot {
			return merged[i].Slot > merged[j].Slot
		}
		return merged[i].Signature > merged[j].Signature
	})
	if len(merged) > maxKeep {
		merged = merged[:maxKeep]
		truncated = true
	}
	return merged, truncated, nil
}

func fetchSolanaTxDetails(ctx context.Context, cache *Cache, rpcs []string, sigs []solSigRow, heads payHeads) ([]*solTxResult, error) {
	out := make([]*solTxResult, len(sigs))
	var g errgroup.Group
	g.SetLimit(paySolTxConcurrency)

	for i, s := range sigs {
		idx, sigRow := i, s
		if cache != nil {
			if cached, ok := cache.Get("sol:tx:" + sigRow.Signature); ok {
				if tx, ok := cached.(solTxResult); ok {
					cp := tx
					out[idx] = &cp
					continue
				}
			}
		}
		g.Go(func() error {
			res, code := solanaRPCFailover(ctx, rpcs, "getTransaction", sigRow.Signature, map[string]any{
				"encoding":                       "jsonParsed",
				"maxSupportedTransactionVersion": 1,
				"commitment":                     "confirmed",
			})
			if code != "" {
				return errors.New(code)
			}
			if len(res) == 0 || string(res) == "null" {
				return nil
			}
			var tx solTxResult
			if err := json.Unmarshal(res, &tx); err != nil {
				return errors.New("bad_response")
			}
			if tx.Slot == 0 {
				tx.Slot = sigRow.Slot
			}
			if tx.BlockTime == nil && sigRow.BlockTime != nil {
				bt := *sigRow.BlockTime
				tx.BlockTime = &bt
			}
			// S1: Only cache finalized transactions for 10m; unfinalized use 4s (payRecentCacheTTL).
			if cache != nil {
				ttl := payRecentCacheTTL
				if heads.Finalized > 0 && tx.Slot > 0 && tx.Slot <= heads.Finalized {
					ttl = paySolFinalizedTxTTL
				}
				cache.SetWithTTL("sol:tx:"+sigRow.Signature, tx, ttl)
			}
			out[idx] = &tx
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

type rawSolTransfer struct {
	txHash       string
	direction    string
	timestamp    string
	rawAmount    *big.Int
	decimals     int
	mint         string
	counterparty string
	slot         uint64
	failed       bool
}

func parseSolTxTransfers(sig string, tx *solTxResult, asset, addr string) []rawSolTransfer {
	if tx == nil {
		return nil
	}
	failed := tx.Meta != nil && tx.Meta.Err != nil
	var tsStr string
	if tx.BlockTime != nil && *tx.BlockTime > 0 {
		tsStr = time.Unix(*tx.BlockTime, 0).UTC().Format(time.RFC3339)
	}
	instructions := tx.flatInstructions()

	// Native SOL (S4: only system program transfer / transferWithSeed; NO pre/post delta fallback).
	if asset == "SOL" {
		var out []rawSolTransfer
		for _, ix := range instructions {
			if ix.Program != "system" && ix.ProgramID != solanaSystemProgramID {
				continue
			}
			var parsed struct {
				Type string `json:"type"`
				Info struct {
					Source      string `json:"source"`
					Destination string `json:"destination"`
					Lamports    uint64 `json:"lamports"`
				} `json:"info"`
			}
			if err := json.Unmarshal(ix.Parsed, &parsed); err != nil {
				continue
			}
			if parsed.Type != "transfer" && parsed.Type != "transferWithSeed" {
				continue
			}
			src, dst := parsed.Info.Source, parsed.Info.Destination
			if src != addr && dst != addr {
				continue
			}
			dir := "in"
			cp := src
			if src == addr {
				dir = "out"
				cp = dst
			}
			out = append(out, rawSolTransfer{
				txHash:       sig,
				direction:    dir,
				timestamp:    tsStr,
				rawAmount:    new(big.Int).SetUint64(parsed.Info.Lamports),
				decimals:     9,
				mint:         "",
				counterparty: cp,
				slot:         tx.Slot,
				failed:       failed,
			})
		}
		return out
	}

	// SPL Tokens (USDT / USDC): map every token account pubkey -> wallet owner (B6).
	keys := tx.resolvedAccountKeys()
	acctToOwner := make(map[string]string)
	acctToMint := make(map[string]string)
	mintDecimals := make(map[string]int)

	for _, tok := range tokensFor(asset, "sol") {
		if ata, err := deriveSolanaATA(addr, tok.Contract); err == nil {
			acctToOwner[ata] = addr
			acctToMint[ata] = tok.Contract
			mintDecimals[tok.Contract] = tok.Decimals
		}
	}

	if tx.Meta != nil {
		for _, tb := range slices.Concat(tx.Meta.PreTokenBalances, tx.Meta.PostTokenBalances) {
			if tb.AccountIndex >= 0 && tb.AccountIndex < len(keys) {
				acct := keys[tb.AccountIndex]
				if tb.Owner != "" {
					acctToOwner[acct] = tb.Owner
				}
				if tb.Mint != "" {
					acctToMint[acct] = tb.Mint
					mintDecimals[tb.Mint] = tb.UITokenAmount.Decimals
				}
			}
		}
	}

	// Also pick up ATA creation instructions in the same tx.
	for _, ix := range instructions {
		if ix.Program == "spl-associated-token-account" {
			var parsed struct {
				Info struct {
					Account string `json:"account"`
					Wallet  string `json:"wallet"`
					Mint    string `json:"mint"`
				} `json:"info"`
			}
			if err := json.Unmarshal(ix.Parsed, &parsed); err == nil && parsed.Info.Account != "" {
				if parsed.Info.Wallet != "" {
					acctToOwner[parsed.Info.Account] = parsed.Info.Wallet
				}
				if parsed.Info.Mint != "" {
					acctToMint[parsed.Info.Account] = parsed.Info.Mint
				}
			}
		}
	}

	var out []rawSolTransfer
	emittedMints := make(map[string]bool)

	for _, ix := range instructions {
		if ix.Program != "spl-token" && ix.Program != "spl-token-2022" &&
			ix.ProgramID != solanaTokenProgramID && ix.ProgramID != solanaToken2022ProgramID {
			continue
		}
		var parsed struct {
			Type string `json:"type"`
			Info struct {
				Source      string `json:"source"`
				Destination string `json:"destination"`
				Authority   string `json:"authority"`
				Mint        string `json:"mint"`
				Amount      string `json:"amount"`
				TokenAmount *struct {
					Amount   string `json:"amount"`
					Decimals int    `json:"decimals"`
				} `json:"tokenAmount"`
			} `json:"info"`
		}
		if err := json.Unmarshal(ix.Parsed, &parsed); err != nil {
			continue
		}
		if parsed.Type != "transfer" && parsed.Type != "transferChecked" {
			continue
		}

		srcOwner := acctToOwner[parsed.Info.Source]
		if srcOwner == "" && parsed.Info.Authority != "" {
			srcOwner = parsed.Info.Authority
		}
		if srcOwner == "" {
			srcOwner = parsed.Info.Source
		}
		dstOwner := acctToOwner[parsed.Info.Destination]
		if dstOwner == "" {
			dstOwner = parsed.Info.Destination
		}
		if srcOwner != addr && dstOwner != addr {
			continue
		}

		mint := parsed.Info.Mint
		if mint == "" {
			mint = acctToMint[parsed.Info.Source]
		}
		if mint == "" {
			mint = acctToMint[parsed.Info.Destination]
		}

		amtStr := parsed.Info.Amount
		dec := mintDecimals[mint]
		if parsed.Info.TokenAmount != nil {
			amtStr = parsed.Info.TokenAmount.Amount
			dec = parsed.Info.TokenAmount.Decimals
		}
		if dec <= 0 {
			dec = 6
		}
		rawAmt, ok := new(big.Int).SetString(strings.TrimSpace(amtStr), 10)
		if !ok {
			continue
		}

		dir := "in"
		cp := srcOwner
		if srcOwner == addr {
			dir = "out"
			cp = dstOwner
		}
		emittedMints[mint] = true
		out = append(out, rawSolTransfer{
			txHash:       sig,
			direction:    dir,
			timestamp:    tsStr,
			rawAmount:    rawAmt,
			decimals:     dec,
			mint:         mint,
			counterparty: cp,
			slot:         tx.Slot,
			failed:       failed,
		})
	}

	// Net pre/post token balance fallback ONLY for (addr, mint) pairs not already emitted by transfer instructions.
	if tx.Meta != nil && !failed {
		type ownerMint struct {
			owner string
			mint  string
		}
		netByOwnerMint := make(map[ownerMint]*big.Int)
		for _, tb := range tx.Meta.PreTokenBalances {
			if tb.Owner == "" || tb.Mint == "" {
				continue
			}
			amt, ok := new(big.Int).SetString(tb.UITokenAmount.Amount, 10)
			if !ok {
				continue
			}
			k := ownerMint{owner: tb.Owner, mint: tb.Mint}
			if netByOwnerMint[k] == nil {
				netByOwnerMint[k] = big.NewInt(0)
			}
			netByOwnerMint[k].Sub(netByOwnerMint[k], amt)
		}
		for _, tb := range tx.Meta.PostTokenBalances {
			if tb.Owner == "" || tb.Mint == "" {
				continue
			}
			amt, ok := new(big.Int).SetString(tb.UITokenAmount.Amount, 10)
			if !ok {
				continue
			}
			k := ownerMint{owner: tb.Owner, mint: tb.Mint}
			if netByOwnerMint[k] == nil {
				netByOwnerMint[k] = big.NewInt(0)
			}
			netByOwnerMint[k].Add(netByOwnerMint[k], amt)
		}

		for k, delta := range netByOwnerMint {
			if k.owner != addr || delta.Sign() == 0 || emittedMints[k.mint] {
				continue
			}
			dir := "in"
			absAmt := new(big.Int).Abs(delta)
			if delta.Sign() < 0 {
				dir = "out"
			}
			// Find opposite-sign wallet owner for this mint (B6).
			var bestCP string
			bestMag := big.NewInt(0)
			for ok2, d2 := range netByOwnerMint {
				if ok2.mint != k.mint || ok2.owner == addr || d2.Sign() == 0 {
					continue
				}
				if (delta.Sign() > 0 && d2.Sign() < 0) || (delta.Sign() < 0 && d2.Sign() > 0) {
					mag := new(big.Int).Abs(d2)
					if mag.Cmp(bestMag) > 0 {
						bestMag = mag
						bestCP = ok2.owner
					}
				}
			}
			dec := mintDecimals[k.mint]
			if dec <= 0 {
				dec = 6
			}
			out = append(out, rawSolTransfer{
				txHash:       sig,
				direction:    dir,
				timestamp:    tsStr,
				rawAmount:    absAmt,
				decimals:     dec,
				mint:         k.mint,
				counterparty: bestCP,
				slot:         tx.Slot,
				failed:       false,
			})
		}
	}

	return out
}

func resolveSolanaUnknownMintSymbols(ctx context.Context, cache *Cache, rpcs []string, mints []string) map[string]string {
	symbols := make(map[string]string, len(mints))
	var uncachedMints []string
	var uncachedPDAs []string

	for _, mint := range mints {
		if mint == "" {
			continue
		}
		if cache != nil {
			if cached, ok := cache.Get("sol:mintmeta:" + mint); ok {
				symbols[mint] = cached.(string)
				continue
			}
		}
		pda, err := deriveSolanaMetadataPDA(mint)
		if err != nil {
			continue
		}
		uncachedMints = append(uncachedMints, mint)
		uncachedPDAs = append(uncachedPDAs, pda)
	}

	if len(uncachedPDAs) == 0 || len(rpcs) == 0 {
		return symbols
	}

	res, code := solanaRPCFailover(ctx, rpcs, "getMultipleAccounts", uncachedPDAs, map[string]any{
		"encoding":   "base64",
		"commitment": "confirmed",
	})
	if code != "" {
		return symbols
	}
	var out struct {
		Value []*struct {
			Data []string `json:"data"`
		} `json:"value"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return symbols
	}

	for i, mint := range uncachedMints {
		var sym string
		if i < len(out.Value) && out.Value[i] != nil && len(out.Value[i].Data) >= 1 {
			if raw, err := base64.StdEncoding.DecodeString(out.Value[i].Data[0]); err == nil {
				sym = parseMetaplexSymbol(raw)
			}
		}
		symbols[mint] = sym
		if cache != nil {
			cache.SetWithTTL("sol:mintmeta:"+mint, sym, paySolMintMetaTTL)
		}
	}
	return symbols
}

func finalizeSolanaPayTxs(ctx context.Context, cache *Cache, rpcs []string, raw []rawSolTransfer, asset string) []payTx {
	if len(raw) == 0 {
		return nil
	}
	info := chains["sol"]

	var unknownMints []string
	seenUnknown := make(map[string]bool)
	for _, r := range raw {
		if r.mint == "" {
			continue
		}
		if _, isReg := lookupRegistryToken("sol", r.mint); !isReg && !seenUnknown[r.mint] {
			seenUnknown[r.mint] = true
			unknownMints = append(unknownMints, r.mint)
		}
	}
	mintSymbols := resolveSolanaUnknownMintSymbols(ctx, cache, rpcs, unknownMints)

	out := make([]payTx, 0, len(raw))
	for _, r := range raw {
		symbol := "SOL"
		tier := tierNative
		decimals := r.decimals

		if asset != "SOL" {
			if regTok, isReg := lookupRegistryToken("sol", r.mint); isReg {
				symbol = regTok.Label
				decimals = regTok.Decimals
				if strings.EqualFold(regTok.Asset, asset) {
					tier = regTok.Tier
				} else {
					tier = tierOther
				}
			} else {
				sym := mintSymbols[r.mint]
				if sym != "" && normalizeMimicSymbol(sym) == asset {
					symbol = sym
					tier = tierCounterfeit
				} else {
					if sym == "" {
						sym = r.mint[:min(6, len(r.mint))]
					}
					symbol = sym
					tier = tierOther
				}
			}
		}

		out = append(out, payTx{
			TxHash:        r.txHash,
			ExplorerURL:   fmt.Sprintf(info.TxURL, r.txHash),
			Direction:     r.direction,
			Timestamp:     r.timestamp,
			Amount:        formatUnits(r.rawAmount, decimals),
			Symbol:        symbol,
			TokenContract: r.mint,
			TokenTier:     tier,
			Counterparty:  r.counterparty,
			Block:         r.slot,
			Failed:        r.failed,
			Flags:         []string{},
			rawValue:      r.rawAmount.String(),
		})
	}
	sortPayTxsDesc(out)
	return out
}

// fetchSolanaRecent fetches up to paySolRecentMaxTxs recent transactions for /live.
func fetchSolanaRecent(ctx context.Context, cache *Cache, rpcs []string, asset, addr string, heads payHeads) ([]payTx, error) {
	if len(rpcs) == 0 {
		return nil, errors.New("not_configured")
	}
	ctx, cancel := context.WithTimeout(ctx, payUpstreamTimeout)
	defer cancel()

	sigs, _, err := collectSolanaSignatures(ctx, rpcs, asset, addr, 0, paySolRecentMaxTxs, paySolSigPageSize)
	if err != nil {
		return nil, err
	}
	if len(sigs) == 0 {
		return []payTx{}, nil
	}

	details, err := fetchSolanaTxDetails(ctx, cache, rpcs, sigs, heads)
	if err != nil {
		return nil, err
	}

	var raw []rawSolTransfer
	for i, tx := range details {
		raw = append(raw, parseSolTxTransfers(sigs[i].Signature, tx, asset, addr)...)
	}
	return finalizeSolanaPayTxs(ctx, cache, rpcs, raw, asset), nil
}

// fetchSolanaHistory fetches up to paySolHistoryMaxTxs transactions across the
// 7-day window for /history, setting scope.Truncated when exceeding cap.
func fetchSolanaHistory(ctx context.Context, cache *Cache, rpcs []string, asset, addr string, since time.Time, heads payHeads) ([]payTx, payHistoryScope, error) {
	scope := payHistoryScope{Hosts: hostsOf(rpcs...)}
	if len(rpcs) == 0 {
		return nil, scope, errors.New("not_configured")
	}
	ctx, cancel := context.WithTimeout(ctx, payTronHistoryTimeout)
	defer cancel()

	sigs, trunc, err := collectSolanaSignatures(ctx, rpcs, asset, addr, since.Unix(), paySolHistoryMaxTxs, paySolSigPageSize)
	scope.Truncated = trunc
	scope.Transactions = len(sigs)
	if err != nil {
		return nil, scope, err
	}
	if len(sigs) == 0 {
		return []payTx{}, scope, nil
	}

	details, err := fetchSolanaTxDetails(ctx, cache, rpcs, sigs, heads)
	if err != nil {
		return nil, scope, err
	}

	var raw []rawSolTransfer
	for i, tx := range details {
		raw = append(raw, parseSolTxTransfers(sigs[i].Signature, tx, asset, addr)...)
	}
	return finalizeSolanaPayTxs(ctx, cache, rpcs, raw, asset), scope, nil
}
