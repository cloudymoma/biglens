package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

type payBalanceRow struct {
	Label       string    `json:"label"`
	Contract    string    `json:"contract"`
	Tier        tokenTier `json:"tier"`
	Finalized   string    `json:"finalized"`
	SafeDelta   string    `json:"safe_delta"`
	LatestDelta string    `json:"latest_delta"`
	Error       string    `json:"error,omitempty"`
}

func formatSignedUnits(d *big.Int, decimals int) string {
	if d == nil || d.Sign() == 0 {
		return "0"
	}
	if d.Sign() > 0 {
		return "+" + formatUnits(d, decimals)
	}
	return "-" + formatUnits(d, decimals)
}

func pad32EVMAddr(addr string) string {
	clean := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(addr)), "0x")
	if len(clean) >= 64 {
		return clean[len(clean)-64:]
	}
	return strings.Repeat("0", 64-len(clean)) + clean
}

func parseHexBigInt(s string) (*big.Int, bool) {
	raw := strings.TrimSpace(s)
	digits := strings.TrimPrefix(strings.TrimPrefix(raw, "0x"), "0X")
	if digits == "" || len(digits) == len(raw) {
		return nil, false
	}
	n, ok := new(big.Int).SetString(digits, 16)
	if !ok {
		return nil, false
	}
	return n, true
}

func fetchEVMBalanceTagOnce(ctx context.Context, rpcURL, contract, addr, tag string) (*big.Int, string) {
	var reqPayload map[string]any
	if contract == "" {
		reqPayload = map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "eth_getBalance",
			"params":  []any{addr, tag},
		}
	} else {
		reqPayload = map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "eth_call",
			"params": []any{
				map[string]string{
					"to":   contract,
					"data": "0x70a08231" + pad32EVMAddr(addr),
				},
				tag,
			},
		}
	}
	body, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, "bad_request"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(body))
	if err != nil {
		return nil, "bad_request"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", evmUserAgent)
	resp, err := riskHTTPClient.Do(req)
	if err != nil {
		return nil, upstreamErrCode(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, "rate_limited"
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Sprintf("upstream_http_%d", resp.StatusCode)
	}
	raw, err := readCapped(resp.Body, riskMaxBody)
	if err != nil {
		return nil, "bad_response"
	}
	var out struct {
		Result *string `json:"result"`
		Error  *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Error != nil || out.Result == nil {
		return nil, "bad_response"
	}
	n, ok := parseHexBigInt(*out.Result)
	if !ok {
		return nil, "bad_response"
	}
	return n, ""
}

func fetchEVMBalanceTagWithFailover(ctx context.Context, rpcs []string, contract, addr, tag string) (*big.Int, string) {
	if len(rpcs) == 0 {
		return nil, "not_configured"
	}
	deadline := time.Now().Add(payUpstreamTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	lastCode := "unavailable"
	for i, u := range rpcs {
		rem := time.Until(deadline)
		if rem <= 0 {
			return nil, "timeout"
		}
		tryCtx, cancel := context.WithTimeout(ctx, rem/time.Duration(len(rpcs)-i))
		val, code := fetchEVMBalanceTagOnce(tryCtx, u, contract, addr, tag)
		cancel()
		if code == "" {
			return val, ""
		}
		lastCode = code
	}
	return nil, lastCode
}

// fetchEVMBalances queries finalized, safe, and latest balances for all
// registry contracts of asset on network (or native ETH when asset == "ETH").
// Concurrency is bounded to 4 and each call fails over across rpcs in order.
// Failures degrade per row so a broken bridged token never hides the native row.
func fetchEVMBalances(ctx context.Context, rpcs []string, asset, network, addr string) []payBalanceRow {
	type targetSpec struct {
		label    string
		contract string
		tier     tokenTier
		decimals int
	}
	var specs []targetSpec
	if asset == "ETH" {
		specs = []targetSpec{{label: "ETH", contract: "", tier: tierNative, decimals: 18}}
	} else {
		for _, tok := range tokensFor(asset, network) {
			specs = append(specs, targetSpec{
				label:    tok.Label,
				contract: tok.Contract,
				tier:     tok.Tier,
				decimals: tok.Decimals,
			})
		}
	}
	if len(specs) == 0 {
		return nil
	}

	tags := [3]string{"latest", "safe", "finalized"}
	vals := make([][3]*big.Int, len(specs))
	codes := make([][3]string, len(specs))

	var g errgroup.Group
	g.SetLimit(4)
	for rowIdx, sp := range specs {
		for tagIdx, tag := range tags {
			rIdx, tIdx := rowIdx, tagIdx
			contract, tagStr := sp.contract, tag
			g.Go(func() error {
				v, c := fetchEVMBalanceTagWithFailover(ctx, rpcs, contract, addr, tagStr)
				vals[rIdx][tIdx] = v
				codes[rIdx][tIdx] = c
				return nil
			})
		}
	}
	_ = g.Wait()

	rows := make([]payBalanceRow, len(specs))
	for i, sp := range specs {
		row := payBalanceRow{
			Label:    sp.label,
			Contract: sp.contract,
			Tier:     sp.tier,
		}
		var errCode string
		for _, c := range codes[i] {
			if c != "" {
				errCode = c
				break
			}
		}
		if errCode != "" {
			row.Error = errCode
			rows[i] = row
			continue
		}
		latVal, safeVal, finVal := vals[i][0], vals[i][1], vals[i][2]
		row.Finalized = formatUnits(finVal, sp.decimals)
		row.SafeDelta = formatSignedUnits(new(big.Int).Sub(safeVal, finVal), sp.decimals)
		row.LatestDelta = formatSignedUnits(new(big.Int).Sub(latVal, safeVal), sp.decimals)
		rows[i] = row
	}
	return rows
}

func fetchTronAccountBalanceOnce(ctx context.Context, path, addr string) (*big.Int, string) {
	payload, err := json.Marshal(map[string]any{
		"address": addr,
		"visible": true,
	})
	if err != nil {
		return nil, "bad_request"
	}
	raw, code := tronGridDo(ctx, http.MethodPost, path, payload)
	if code != "" {
		return nil, code
	}
	var parsed struct {
		Balance *int64 `json:"balance"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, "bad_response"
	}
	if parsed.Balance == nil {
		return big.NewInt(0), ""
	}
	if *parsed.Balance < 0 {
		return nil, "bad_response"
	}
	return big.NewInt(*parsed.Balance), ""
}

func fetchTronTRC20BalanceOnce(ctx context.Context, path, contract, addr, hex20 string) (*big.Int, string) {
	payload, err := json.Marshal(map[string]any{
		"owner_address":     addr,
		"contract_address":  contract,
		"function_selector": "balanceOf(address)",
		"parameter":         strings.Repeat("0", 24) + hex20,
		"visible":           true,
	})
	if err != nil {
		return nil, "bad_request"
	}
	raw, code := tronGridDo(ctx, http.MethodPost, path, payload)
	if code != "" {
		return nil, code
	}
	var parsed struct {
		Result struct {
			Result bool `json:"result"`
		} `json:"result"`
		ConstantResult []string `json:"constant_result"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || !parsed.Result.Result || len(parsed.ConstantResult) == 0 {
		return nil, "bad_response"
	}
	resHex := strings.TrimSpace(parsed.ConstantResult[0])
	if !strings.HasPrefix(strings.ToLower(resHex), "0x") {
		resHex = "0x" + resHex
	}
	n, ok := parseHexBigInt(resHex)
	if !ok {
		return nil, "bad_response"
	}
	return n, ""
}

// fetchTronBalances queries solidified and latest balances on TRON for TRX or
// registry TRC-20 contracts. SafeDelta is always "" on TRON.
func fetchTronBalances(ctx context.Context, asset, addr string) []payBalanceRow {
	ctx, cancel := context.WithTimeout(ctx, payUpstreamTimeout)
	defer cancel()

	if asset == "TRX" {
		row := payBalanceRow{Label: "TRX", Contract: "", Tier: tierNative}
		var (
			latVal, solVal   *big.Int
			latCode, solCode string
			wg               sync.WaitGroup
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			latVal, latCode = fetchTronAccountBalanceOnce(ctx, "/wallet/getaccount", addr)
		}()
		go func() {
			defer wg.Done()
			solVal, solCode = fetchTronAccountBalanceOnce(ctx, "/walletsolidity/getaccount", addr)
		}()
		wg.Wait()
		if latCode != "" {
			row.Error = latCode
			return []payBalanceRow{row}
		}
		if solCode != "" {
			row.Error = solCode
			return []payBalanceRow{row}
		}
		row.Finalized = formatUnits(solVal, 6)
		row.SafeDelta = ""
		row.LatestDelta = formatSignedUnits(new(big.Int).Sub(latVal, solVal), 6)
		return []payBalanceRow{row}
	}

	toks := tokensFor(asset, "tron")
	if len(toks) == 0 {
		return nil
	}
	hex20, err := tronBase58ToHex(addr)
	if err != nil {
		rows := make([]payBalanceRow, len(toks))
		for i, tok := range toks {
			rows[i] = payBalanceRow{Label: tok.Label, Contract: tok.Contract, Tier: tok.Tier, Error: "bad_request"}
		}
		return rows
	}

	rows := make([]payBalanceRow, len(toks))
	for i, tok := range toks {
		row := payBalanceRow{Label: tok.Label, Contract: tok.Contract, Tier: tok.Tier}
		var (
			latVal, solVal   *big.Int
			latCode, solCode string
			wg               sync.WaitGroup
		)
		wg.Add(2)
		go func(contract string) {
			defer wg.Done()
			latVal, latCode = fetchTronTRC20BalanceOnce(ctx, "/wallet/triggerconstantcontract", contract, addr, hex20)
		}(tok.Contract)
		go func(contract string) {
			defer wg.Done()
			solVal, solCode = fetchTronTRC20BalanceOnce(ctx, "/walletsolidity/triggerconstantcontract", contract, addr, hex20)
		}(tok.Contract)
		wg.Wait()
		if latCode != "" {
			row.Error = latCode
			rows[i] = row
			continue
		}
		if solCode != "" {
			row.Error = solCode
			rows[i] = row
			continue
		}
		row.Finalized = formatUnits(solVal, tok.Decimals)
		row.SafeDelta = ""
		row.LatestDelta = formatSignedUnits(new(big.Int).Sub(latVal, solVal), tok.Decimals)
		rows[i] = row
	}
	return rows
}

const payBTCBalanceMaxPages = 4

type btcBalanceTx struct {
	TxID   string `json:"txid"`
	Status struct {
		Confirmed   bool   `json:"confirmed"`
		BlockHeight uint64 `json:"block_height"`
	} `json:"status"`
	Vin []struct {
		Prevout *struct {
			ScriptpubkeyAddress string `json:"scriptpubkey_address"`
			Value               int64  `json:"value"`
		} `json:"prevout"`
	} `json:"vin"`
	Vout []struct {
		ScriptpubkeyAddress string `json:"scriptpubkey_address"`
		Value               int64  `json:"value"`
	} `json:"vout"`
}

func btcTxNetForAddr(tx btcBalanceTx, addr string) int64 {
	var net int64
	for _, out := range tx.Vout {
		if out.ScriptpubkeyAddress == addr {
			net += out.Value
		}
	}
	for _, in := range tx.Vin {
		if in.Prevout != nil && in.Prevout.ScriptpubkeyAddress == addr {
			net -= in.Prevout.Value
		}
	}
	return net
}

// fetchBTCBalances computes finalized (>=6 conf), safe_delta (1-5 conf), and
// latest_delta (0 conf mempool) balances in BTC using Esplora address stats and
// recent chain transactions. Fails loud (Error="unavailable") if 1-5 conf txs
// span more than payBTCBalanceMaxPages pages or if computed finalized < 0 (B4).
func fetchBTCBalances(ctx context.Context, addr string, heads payHeads) []payBalanceRow {
	ctx, cancel := context.WithTimeout(ctx, payUpstreamTimeout)
	defer cancel()

	row := payBalanceRow{Label: "BTC", Contract: "", Tier: tierNative}

	var (
		addrRaw, txsRaw   []byte
		addrCode, txsCode string
		wg                sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		addrRaw, addrCode = esploraGet(ctx, "/address/"+addr)
	}()
	go func() {
		defer wg.Done()
		txsRaw, txsCode = esploraGet(ctx, "/address/"+addr+"/txs")
	}()
	wg.Wait()

	if addrCode != "" {
		row.Error = addrCode
		return []payBalanceRow{row}
	}
	if txsCode != "" {
		row.Error = txsCode
		return []payBalanceRow{row}
	}

	var addrInfo struct {
		ChainStats struct {
			FundedTxoSum int64 `json:"funded_txo_sum"`
			SpentTxoSum  int64 `json:"spent_txo_sum"`
		} `json:"chain_stats"`
		MempoolStats struct {
			FundedTxoSum int64 `json:"funded_txo_sum"`
			SpentTxoSum  int64 `json:"spent_txo_sum"`
		} `json:"mempool_stats"`
	}
	if err := json.Unmarshal(addrRaw, &addrInfo); err != nil {
		row.Error = "bad_response"
		return []payBalanceRow{row}
	}

	var pageTxs []btcBalanceTx
	if err := json.Unmarshal(txsRaw, &pageTxs); err != nil {
		row.Error = "bad_response"
		return []payBalanceRow{row}
	}

	var pendingConfirmedSats int64
	crossedFinalized := false

	for page := 1; page <= payBTCBalanceMaxPages; page++ {
		var oldestConfirmedHeight uint64
		var lastTxID string
		hasConfirmed := false

		for _, tx := range pageTxs {
			if !tx.Status.Confirmed {
				continue
			}
			hasConfirmed = true
			oldestConfirmedHeight = tx.Status.BlockHeight
			lastTxID = tx.TxID
			if tx.Status.BlockHeight > heads.Finalized {
				pendingConfirmedSats += btcTxNetForAddr(tx, addr)
			}
		}

		if !hasConfirmed || oldestConfirmedHeight <= heads.Finalized {
			crossedFinalized = true
			break
		}
		if page == payBTCBalanceMaxPages || lastTxID == "" {
			break
		}

		nextRaw, nextCode := esploraGet(ctx, "/address/"+addr+"/txs/chain/"+lastTxID)
		if nextCode != "" {
			row.Error = nextCode
			return []payBalanceRow{row}
		}
		pageTxs = nil
		if err := json.Unmarshal(nextRaw, &pageTxs); err != nil {
			row.Error = "bad_response"
			return []payBalanceRow{row}
		}
	}

	confirmedNet := addrInfo.ChainStats.FundedTxoSum - addrInfo.ChainStats.SpentTxoSum
	mempoolNet := addrInfo.MempoolStats.FundedTxoSum - addrInfo.MempoolStats.SpentTxoSum
	finalizedSats := confirmedNet - pendingConfirmedSats

	if !crossedFinalized || finalizedSats < 0 {
		row.Error = "unavailable"
		return []payBalanceRow{row}
	}

	row.Finalized = formatUnits(big.NewInt(finalizedSats), 8)
	row.SafeDelta = formatSignedUnits(big.NewInt(pendingConfirmedSats), 8)
	row.LatestDelta = formatSignedUnits(big.NewInt(mempoolNet), 8)
	return []payBalanceRow{row}
}

func fetchSolanaNativeBalanceOnce(ctx context.Context, rpcs []string, addr, commitment string) (*big.Int, string) {
	res, code := solanaRPCFailover(ctx, rpcs, "getBalance", addr, map[string]string{"commitment": commitment})
	if code != "" {
		return nil, code
	}
	var out struct {
		Value *uint64 `json:"value"`
	}
	if err := json.Unmarshal(res, &out); err != nil || out.Value == nil {
		return nil, "bad_response"
	}
	return new(big.Int).SetUint64(*out.Value), ""
}

func fetchSolanaSPLBalanceOnce(ctx context.Context, rpcs []string, ata, commitment string) (*big.Int, string) {
	res, code := solanaRPCFailover(ctx, rpcs, "getAccountInfo", ata, map[string]any{
		"encoding":   "jsonParsed",
		"commitment": commitment,
	})
	if code != "" {
		return nil, code
	}
	var out struct {
		Value *struct {
			Data struct {
				Parsed struct {
					Info struct {
						TokenAmount struct {
							Amount string `json:"amount"`
						} `json:"tokenAmount"`
					} `json:"info"`
				} `json:"parsed"`
			} `json:"data"`
		} `json:"value"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, "bad_response"
	}
	if out.Value == nil {
		return big.NewInt(0), ""
	}
	n, ok := new(big.Int).SetString(strings.TrimSpace(out.Value.Data.Parsed.Info.TokenAmount.Amount), 10)
	if !ok {
		return nil, "bad_response"
	}
	return n, ""
}

// fetchSolanaBalances queries processed, confirmed, and finalized balances for
// native SOL or canonical ATA of registry SPL tokens (USDT, USDC).
func fetchSolanaBalances(ctx context.Context, rpcs []string, asset, addr string) []payBalanceRow {
	ctx, cancel := context.WithTimeout(ctx, payUpstreamTimeout)
	defer cancel()

	commitments := [3]string{"processed", "confirmed", "finalized"}

	if asset == "SOL" {
		row := payBalanceRow{Label: "SOL", Contract: "", Tier: tierNative}
		if len(rpcs) == 0 {
			row.Error = "not_configured"
			return []payBalanceRow{row}
		}
		var vals [3]*big.Int
		var codes [3]string
		var wg sync.WaitGroup
		for i, c := range commitments {
			wg.Add(1)
			go func(i int, c string) {
				defer wg.Done()
				vals[i], codes[i] = fetchSolanaNativeBalanceOnce(ctx, rpcs, addr, c)
			}(i, c)
		}
		wg.Wait()
		for _, c := range codes {
			if c != "" {
				row.Error = c
				return []payBalanceRow{row}
			}
		}
		procVal, confVal, finVal := vals[0], vals[1], vals[2]
		row.Finalized = formatUnits(finVal, 9)
		row.SafeDelta = formatSignedUnits(new(big.Int).Sub(confVal, finVal), 9)
		row.LatestDelta = formatSignedUnits(new(big.Int).Sub(procVal, confVal), 9)
		return []payBalanceRow{row}
	}

	toks := tokensFor(asset, "sol")
	if len(toks) == 0 {
		return nil
	}
	rows := make([]payBalanceRow, len(toks))
	for idx, tok := range toks {
		row := payBalanceRow{Label: tok.Label, Contract: tok.Contract, Tier: tok.Tier}
		if len(rpcs) == 0 {
			row.Error = "not_configured"
			rows[idx] = row
			continue
		}
		ata, err := deriveSolanaATA(addr, tok.Contract)
		if err != nil {
			row.Error = "bad_request"
			rows[idx] = row
			continue
		}
		var vals [3]*big.Int
		var codes [3]string
		var wg sync.WaitGroup
		for i, c := range commitments {
			wg.Add(1)
			go func(i int, c string) {
				defer wg.Done()
				vals[i], codes[i] = fetchSolanaSPLBalanceOnce(ctx, rpcs, ata, c)
			}(i, c)
		}
		wg.Wait()
		var errCode string
		for _, c := range codes {
			if c != "" {
				errCode = c
				break
			}
		}
		if errCode != "" {
			row.Error = errCode
			rows[idx] = row
			continue
		}
		procVal, confVal, finVal := vals[0], vals[1], vals[2]
		row.Finalized = formatUnits(finVal, tok.Decimals)
		row.SafeDelta = formatSignedUnits(new(big.Int).Sub(confVal, finVal), tok.Decimals)
		row.LatestDelta = formatSignedUnits(new(big.Int).Sub(procVal, confVal), tok.Decimals)
		rows[idx] = row
	}
	return rows
}
