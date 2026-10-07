package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	payHistoryWindow       = 7 * 24 * time.Hour
	payTronHistoryTimeout  = 20 * time.Second
	payHistoryMaxRows      = 1000
	payRecentPageSize      = 20
	tronPageSize           = 200
	tronMaxTxInfoLookups   = 5
	erc20TransferTopic     = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
	erc20TransferMethodHex = "a9059cbb"
)

// payLogsMaxSpan is the maximum block range per eth_getLogs call; overridable
// via payment_check.logs_max_span in conf.yaml.
var payLogsMaxSpan uint64 = 10000

type payTx struct {
	TxHash           string    `json:"tx_hash"`
	ExplorerURL      string    `json:"explorer_url"`
	Direction        string    `json:"direction"` // in | out
	Timestamp        string    `json:"timestamp"` // RFC3339
	Amount           string    `json:"amount"`
	Symbol           string    `json:"symbol"`
	TokenContract    string    `json:"token_contract"`
	TokenTier        tokenTier `json:"token_tier"`
	Counterparty     string    `json:"counterparty"`
	Block            uint64    `json:"block"`
	Failed           bool      `json:"failed"`
	Level            payLevel  `json:"level"`
	Progress         int       `json:"progress"`
	EstSecLeft       int       `json:"est_sec_left"`
	Flags            []string  `json:"flags"`
	CounterpartyHits []string  `json:"counterparty_hits,omitempty"`
	rawValue         string
}

type payHistoryScope struct {
	Hosts                   []string `json:"hosts"`
	TokenTx                 int      `json:"tokentx,omitempty"`
	TxList                  int      `json:"txlist,omitempty"`
	TxListInternal          int      `json:"txlistinternal,omitempty"`
	TRC20                   int      `json:"trc20,omitempty"`
	Transactions            int      `json:"transactions,omitempty"`
	UnsettledLogs           int      `json:"unsettled_logs,omitempty"`
	Truncated               bool     `json:"truncated"`
	TruncatedFailedOutgoing bool     `json:"truncated_failed_outgoing,omitempty"`
}

// blockscoutTxRow is Payment Check's dedicated row struct for Blockscout's
// Etherscan-compatible account actions (review-2 #3); etherscanRow in Address
// Risk remains untouched.
type blockscoutTxRow struct {
	Hash            string `json:"hash"`
	TransactionHash string `json:"transactionHash"`
	From            string `json:"from"`
	To              string `json:"to"`
	Value           string `json:"value"`
	IsError         string `json:"isError"`
	TimeStamp       string `json:"timeStamp"`
	ContractAddress string `json:"contractAddress"`
	BlockNumber     string `json:"blockNumber"`
	Input           string `json:"input"`
	TokenSymbol     string `json:"tokenSymbol"`
	TokenDecimal    string `json:"tokenDecimal"`
}

// decodeTransferCalldata decodes ERC-20 / TRC-20 transfer(address,uint256)
// calldata (selector 0xa9059cbb + two 32-byte words = 136 hex chars). Any
// shorter input returns ok=false without slicing past bounds (review-2 #3).
func decodeTransferCalldata(hexData string) (to20 string, amount *big.Int, ok bool) {
	clean := strings.ToLower(strings.TrimSpace(hexData))
	clean = strings.TrimPrefix(clean, "0x")
	if len(clean) < 136 || !strings.HasPrefix(clean, erc20TransferMethodHex) {
		return "", nil, false
	}
	toWord := clean[8:72]
	amtWord := clean[72:136]
	if _, validTo := new(big.Int).SetString(toWord, 16); !validTo {
		return "", nil, false
	}
	amt, validAmt := new(big.Int).SetString(amtWord, 16)
	if !validAmt {
		return "", nil, false
	}
	return toWord[24:64], amt, true
}

func blockscoutTxListAt(ctx context.Context, baseURL, action, addr string, offset int) ([]blockscoutTxRow, string) {
	if baseURL == "" {
		return nil, "not_configured"
	}
	q := url.Values{
		"module":  {"account"},
		"action":  {action},
		"address": {addr},
		"page":    {"1"},
		"offset":  {strconv.Itoa(offset)},
		"sort":    {"desc"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api?"+q.Encode(), nil)
	if err != nil {
		return nil, "bad_request"
	}
	req.Header.Set("User-Agent", blockscoutUserAgent)
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
	body, err := readCapped(resp.Body, 96<<20)
	if err != nil {
		return nil, "bad_response"
	}
	var env etherscanEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, "bad_response"
	}
	if (env.Status == "1" || env.Status == "2" || env.Status == "0") &&
		strings.HasPrefix(strings.TrimSpace(string(env.Result)), "[") {
		var rows []blockscoutTxRow
		if err := json.Unmarshal(env.Result, &rows); err != nil {
			return nil, "bad_response"
		}
		for i := range rows {
			if rows[i].Hash == "" && rows[i].TransactionHash != "" {
				rows[i].Hash = rows[i].TransactionHash
			}
		}
		return rows, ""
	}
	return nil, "bad_response"
}

func parseUpstreamDecimals(s string, fallback int) int {
	d, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || d < 0 || d > 36 {
		return fallback
	}
	return d
}

func convertEVMTokenTxRows(rows []blockscoutTxRow, asset, network, addr string, sinceSec int64) ([]payTx, int, bool) {
	info := chains[network]
	var out []payTx
	kept := 0
	sawOlder := false
	for _, r := range rows {
		ts, err := strconv.ParseInt(strings.TrimSpace(r.TimeStamp), 10, 64)
		if err != nil {
			continue
		}
		if sinceSec > 0 && ts < sinceSec {
			sawOlder = true
			continue
		}
		kept++
		from := strings.ToLower(strings.TrimSpace(r.From))
		to := strings.ToLower(strings.TrimSpace(r.To))
		contract := strings.ToLower(strings.TrimSpace(r.ContractAddress))
		hash := strings.ToLower(strings.TrimSpace(r.Hash))

		var dir, cp string
		switch {
		case from == addr:
			dir, cp = "out", to
		case to == addr:
			dir, cp = "in", from
		default:
			continue
		}

		v, ok := new(big.Int).SetString(strings.TrimSpace(r.Value), 10)
		if !ok {
			continue
		}

		var (
			tier     tokenTier
			symbol   string
			decimals int
		)
		if regTok, isReg := lookupRegistryToken(network, contract); isReg {
			decimals = regTok.Decimals
			symbol = regTok.Label
			if regTok.Asset == asset {
				tier = regTok.Tier
			} else {
				tier = tierOther
			}
		} else {
			decimals = parseUpstreamDecimals(r.TokenDecimal, 6)
			upSym := strings.TrimSpace(r.TokenSymbol)
			if normalizeMimicSymbol(upSym) == asset {
				tier = tierCounterfeit
				symbol = fmt.Sprintf("%q", upSym)
			} else {
				tier = tierOther
				symbol = upSym
			}
		}

		blk, _ := strconv.ParseUint(strings.TrimSpace(r.BlockNumber), 10, 64)
		out = append(out, payTx{
			TxHash:        hash,
			ExplorerURL:   fmt.Sprintf(info.TxURL, hash),
			Direction:     dir,
			Timestamp:     time.Unix(ts, 0).UTC().Format(time.RFC3339),
			Amount:        formatUnits(v, decimals),
			Symbol:        symbol,
			TokenContract: contract,
			TokenTier:     tier,
			Counterparty:  cp,
			Block:         blk,
			Flags:         []string{},
			rawValue:      v.String(),
		})
	}
	truncated := len(rows) >= payHistoryMaxRows && !sawOlder
	return out, kept, truncated
}

func convertEVMFailedTokenOutRows(rows []blockscoutTxRow, asset, network, addr string, sinceSec int64) ([]payTx, int, bool) {
	info := chains[network]
	var out []payTx
	kept := 0
	sawOlder := false
	for _, r := range rows {
		ts, err := strconv.ParseInt(strings.TrimSpace(r.TimeStamp), 10, 64)
		if err != nil {
			continue
		}
		if sinceSec > 0 && ts < sinceSec {
			sawOlder = true
			continue
		}
		kept++
		if r.IsError != "1" {
			continue
		}
		from := strings.ToLower(strings.TrimSpace(r.From))
		if from != addr {
			continue
		}
		contract := strings.ToLower(strings.TrimSpace(r.To))
		regTok, isReg := lookupRegistryToken(network, contract)
		if !isReg || regTok.Asset != asset {
			continue
		}
		to20, amt, ok := decodeTransferCalldata(r.Input)
		if !ok {
			continue
		}
		hash := strings.ToLower(strings.TrimSpace(r.Hash))
		blk, _ := strconv.ParseUint(strings.TrimSpace(r.BlockNumber), 10, 64)
		out = append(out, payTx{
			TxHash:        hash,
			ExplorerURL:   fmt.Sprintf(info.TxURL, hash),
			Direction:     "out",
			Timestamp:     time.Unix(ts, 0).UTC().Format(time.RFC3339),
			Amount:        formatUnits(amt, regTok.Decimals),
			Symbol:        regTok.Label,
			TokenContract: regTok.Contract,
			TokenTier:     regTok.Tier,
			Counterparty:  "0x" + to20,
			Block:         blk,
			Failed:        true,
			Flags:         []string{},
			rawValue:      amt.String(),
		})
	}
	truncated := len(rows) >= payHistoryMaxRows && !sawOlder
	return out, kept, truncated
}

func convertEVMEthRows(txlist, internal []blockscoutTxRow, network, addr string, sinceSec int64) ([]payTx, int, int, bool) {
	info := chains[network]
	var out []payTx
	keptTx, keptInt := 0, 0
	sawOlderTx, sawOlderInt := false, false

	for _, r := range txlist {
		ts, err := strconv.ParseInt(strings.TrimSpace(r.TimeStamp), 10, 64)
		if err != nil {
			continue
		}
		if sinceSec > 0 && ts < sinceSec {
			sawOlderTx = true
			continue
		}
		keptTx++
		from := strings.ToLower(strings.TrimSpace(r.From))
		to := strings.ToLower(strings.TrimSpace(r.To))
		created := strings.ToLower(strings.TrimSpace(r.ContractAddress))
		hash := strings.ToLower(strings.TrimSpace(r.Hash))
		v, ok := new(big.Int).SetString(strings.TrimSpace(r.Value), 10)
		if !ok {
			continue
		}
		failed := r.IsError != "" && r.IsError != "0"

		var dir, cp string
		switch {
		case from == addr:
			if v.Sign() == 0 && !failed {
				continue
			}
			dir = "out"
			cp = to
			if cp == "" {
				cp = created
			}
		case to == addr:
			if failed || v.Sign() == 0 {
				continue
			}
			dir, cp = "in", from
		default:
			continue
		}

		blk, _ := strconv.ParseUint(strings.TrimSpace(r.BlockNumber), 10, 64)
		out = append(out, payTx{
			TxHash:       hash,
			ExplorerURL:  fmt.Sprintf(info.TxURL, hash),
			Direction:    dir,
			Timestamp:    time.Unix(ts, 0).UTC().Format(time.RFC3339),
			Amount:       formatUnits(v, 18),
			Symbol:       "ETH",
			TokenTier:    tierNative,
			Counterparty: cp,
			Block:        blk,
			Failed:       failed,
			Flags:        []string{},
			rawValue:     v.String(),
		})
	}

	for _, r := range internal {
		ts, err := strconv.ParseInt(strings.TrimSpace(r.TimeStamp), 10, 64)
		if err != nil {
			continue
		}
		if sinceSec > 0 && ts < sinceSec {
			sawOlderInt = true
			continue
		}
		keptInt++
		if r.IsError != "" && r.IsError != "0" {
			continue
		}
		from := strings.ToLower(strings.TrimSpace(r.From))
		to := strings.ToLower(strings.TrimSpace(r.To))
		created := strings.ToLower(strings.TrimSpace(r.ContractAddress))
		if to != addr && !(to == "" && created == addr) {
			continue
		}
		v, ok := new(big.Int).SetString(strings.TrimSpace(r.Value), 10)
		if !ok || v.Sign() == 0 {
			continue
		}
		hash := strings.ToLower(strings.TrimSpace(r.Hash))
		blk, _ := strconv.ParseUint(strings.TrimSpace(r.BlockNumber), 10, 64)
		out = append(out, payTx{
			TxHash:       hash,
			ExplorerURL:  fmt.Sprintf(info.TxURL, hash),
			Direction:    "in",
			Timestamp:    time.Unix(ts, 0).UTC().Format(time.RFC3339),
			Amount:       formatUnits(v, 18),
			Symbol:       "ETH",
			TokenTier:    tierNative,
			Counterparty: from,
			Block:        blk,
			Flags:        []string{},
			rawValue:     v.String(),
		})
	}

	truncated := (len(txlist) >= payHistoryMaxRows && !sawOlderTx) || (len(internal) >= payHistoryMaxRows && !sawOlderInt)
	sortPayTxsDesc(out)
	return out, keptTx, keptInt, truncated
}

func sortPayTxsDesc(txs []payTx) {
	allHaveBlock := true
	for i := range txs {
		if txs[i].Block == 0 {
			allHaveBlock = false
			break
		}
	}
	sort.SliceStable(txs, func(i, j int) bool {
		if allHaveBlock {
			if txs[i].Block != txs[j].Block {
				return txs[i].Block > txs[j].Block
			}
			if txs[i].Timestamp != txs[j].Timestamp {
				return txs[i].Timestamp > txs[j].Timestamp
			}
			return txs[i].TxHash > txs[j].TxHash
		}
		if txs[i].Timestamp != txs[j].Timestamp {
			return txs[i].Timestamp > txs[j].Timestamp
		}
		if txs[i].Block != txs[j].Block {
			return txs[i].Block > txs[j].Block
		}
		return txs[i].TxHash > txs[j].TxHash
	})
}

// fetchEVMHistory fetches the 7-day transaction history for asset on network
// from Blockscout, returning raw rows (before settlement classification and flags).
func fetchEVMHistory(ctx context.Context, blockscoutURL, asset, network, addr string, since time.Time) ([]payTx, payHistoryScope, error) {
	ctx, cancel := context.WithTimeout(ctx, payUpstreamTimeout)
	defer cancel()
	addr = strings.ToLower(strings.TrimSpace(addr))
	sinceSec := since.Unix()
	scope := payHistoryScope{Hosts: hostsOf(blockscoutURL)}

	if asset == "ETH" {
		var (
			txRows, intRows []blockscoutTxRow
			txCode, intCode string
			wg              sync.WaitGroup
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			txRows, txCode = blockscoutTxListAt(ctx, blockscoutURL, "txlist", addr, payHistoryMaxRows)
		}()
		go func() {
			defer wg.Done()
			intRows, intCode = blockscoutTxListAt(ctx, blockscoutURL, "txlistinternal", addr, payHistoryMaxRows)
		}()
		wg.Wait()
		if txCode != "" {
			return nil, scope, errors.New(txCode)
		}
		if intCode != "" {
			return nil, scope, errors.New(intCode)
		}
		txs, nTx, nInt, trunc := convertEVMEthRows(txRows, intRows, network, addr, sinceSec)
		scope.TxList = nTx
		scope.TxListInternal = nInt
		scope.Truncated = trunc
		return txs, scope, nil
	}

	var (
		tokRows, txRows []blockscoutTxRow
		tokCode, txCode string
		wg              sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		tokRows, tokCode = blockscoutTxListAt(ctx, blockscoutURL, "tokentx", addr, payHistoryMaxRows)
	}()
	go func() {
		defer wg.Done()
		txRows, txCode = blockscoutTxListAt(ctx, blockscoutURL, "txlist", addr, payHistoryMaxRows)
	}()
	wg.Wait()
	if tokCode != "" {
		return nil, scope, errors.New(tokCode)
	}
	if txCode != "" {
		return nil, scope, errors.New(txCode)
	}

	tokTxs, nTok, truncTok := convertEVMTokenTxRows(tokRows, asset, network, addr, sinceSec)
	failTxs, nTx, truncTx := convertEVMFailedTokenOutRows(txRows, asset, network, addr, sinceSec)
	scope.TokenTx = nTok
	scope.TxList = nTx
	scope.Truncated = truncTok || truncTx

	combined := append(tokTxs, failTxs...)
	sortPayTxsDesc(combined)
	return combined, scope, nil
}

// fetchEVMRecent fetches the newest 20 rows (page 1 only) for the lightweight
// recent-transactions check in /live (spec §5.6).
func fetchEVMRecent(ctx context.Context, blockscoutURL, asset, network, addr string) ([]payTx, error) {
	ctx, cancel := context.WithTimeout(ctx, payUpstreamTimeout)
	defer cancel()
	addr = strings.ToLower(strings.TrimSpace(addr))

	if asset == "ETH" {
		var (
			txRows, intRows []blockscoutTxRow
			txCode, intCode string
			wg              sync.WaitGroup
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			txRows, txCode = blockscoutTxListAt(ctx, blockscoutURL, "txlist", addr, payRecentPageSize)
		}()
		go func() {
			defer wg.Done()
			intRows, intCode = blockscoutTxListAt(ctx, blockscoutURL, "txlistinternal", addr, payRecentPageSize)
		}()
		wg.Wait()
		if txCode != "" {
			return nil, errors.New(txCode)
		}
		if intCode != "" {
			return nil, errors.New(intCode)
		}
		txs, _, _, _ := convertEVMEthRows(txRows, intRows, network, addr, 0)
		return txs, nil
	}

	tokRows, tokCode := blockscoutTxListAt(ctx, blockscoutURL, "tokentx", addr, payRecentPageSize)
	if tokCode != "" {
		return nil, errors.New(tokCode)
	}
	txs, _, _ := convertEVMTokenTxRows(tokRows, asset, network, addr, 0)
	return txs, nil
}

type evmLogEntry struct {
	Address         string   `json:"address"`
	Topics          []string `json:"topics"`
	Data            string   `json:"data"`
	BlockNumber     string   `json:"blockNumber"`
	BlockTimestamp  string   `json:"blockTimestamp"`
	TransactionHash string   `json:"transactionHash"`
	Removed         bool     `json:"removed"`
}

func fetchEVMLogsSegmentOnce(ctx context.Context, rpcURL string, addresses []string, addrTopic string, fromBlock, toBlock uint64) ([]evmLogEntry, string) {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "eth_getLogs",
		"params": []any{
			map[string]any{
				"fromBlock": fmt.Sprintf("0x%x", fromBlock),
				"toBlock":   fmt.Sprintf("0x%x", toBlock),
				"address":   addresses,
				"topics":    []any{erc20TransferTopic, nil, addrTopic},
			},
		},
	})
	if err != nil {
		return nil, "bad_request"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(payload))
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
	raw, err := readCapped(resp.Body, 4<<20)
	if err != nil {
		return nil, "bad_response"
	}
	var out struct {
		Result []evmLogEntry `json:"result"`
		Error  *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Error != nil || out.Result == nil {
		return nil, "bad_response"
	}
	return out.Result, ""
}

func fetchEVMLogsSegmentWithFailover(ctx context.Context, rpcs []string, addresses []string, addrTopic string, fromBlock, toBlock uint64) ([]evmLogEntry, string) {
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
		logs, code := fetchEVMLogsSegmentOnce(tryCtx, u, addresses, addrTopic, fromBlock, toBlock)
		cancel()
		if code == "" {
			return logs, ""
		}
		lastCode = code
	}
	return nil, lastCode
}

// fetchEVMUnsettled queries eth_getLogs for incoming Transfer events on
// registry contracts in [fromBlock, toBlock], splitting the range into chunks
// of at most payLogsMaxSpan blocks (spec §5.6, review P1-7).
func fetchEVMUnsettled(ctx context.Context, rpcs []string, asset, network, addr string, fromBlock, toBlock uint64) ([]payTx, error) {
	if asset == "ETH" || fromBlock > toBlock {
		return nil, nil
	}
	toks := tokensFor(asset, network)
	if len(toks) == 0 {
		return nil, nil
	}
	addresses := make([]string, len(toks))
	for i, tok := range toks {
		addresses[i] = tok.Contract
	}
	addr = strings.ToLower(strings.TrimSpace(addr))
	addrTopic := "0x" + pad32EVMAddr(addr)
	span := payLogsMaxSpan
	if span == 0 {
		span = 10000
	}

	info := chains[network]
	var out []payTx
	for cur := fromBlock; cur <= toBlock; {
		segEnd := toBlock
		if toBlock-cur >= span {
			segEnd = cur + span - 1
		}
		logs, code := fetchEVMLogsSegmentWithFailover(ctx, rpcs, addresses, addrTopic, cur, segEnd)
		if code != "" {
			return nil, errors.New(code)
		}
		for _, lg := range logs {
			if lg.Removed || len(lg.Topics) < 3 {
				continue
			}
			contract := strings.ToLower(strings.TrimSpace(lg.Address))
			regTok, isReg := lookupRegistryToken(network, contract)
			if !isReg {
				continue
			}
			fromTopic := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(lg.Topics[1])), "0x")
			toTopic := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(lg.Topics[2])), "0x")
			if len(fromTopic) < 40 || len(toTopic) < 40 {
				continue
			}
			fromAddr := "0x" + fromTopic[len(fromTopic)-40:]
			toAddr := "0x" + toTopic[len(toTopic)-40:]
			if toAddr != addr {
				continue
			}
			dataHex := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(lg.Data), "0x"), "0X")
			val := big.NewInt(0)
			if dataHex != "" {
				parsedVal, ok := new(big.Int).SetString(dataHex, 16)
				if !ok {
					continue
				}
				val = parsedVal
			}
			blk, _ := parseHexUint64(lg.BlockNumber)
			var tsStr string
			if lg.BlockTimestamp != "" {
				if tsSec, ok := parseHexUint64(lg.BlockTimestamp); ok && tsSec > 0 {
					tsStr = time.Unix(int64(tsSec), 0).UTC().Format(time.RFC3339)
				} else if tsDec, err := strconv.ParseInt(lg.BlockTimestamp, 10, 64); err == nil && tsDec > 0 {
					tsStr = time.Unix(tsDec, 0).UTC().Format(time.RFC3339)
				}
			}
			txHash := strings.ToLower(strings.TrimSpace(lg.TransactionHash))
			out = append(out, payTx{
				TxHash:        txHash,
				ExplorerURL:   fmt.Sprintf(info.TxURL, txHash),
				Direction:     "in",
				Timestamp:     tsStr,
				Amount:        formatUnits(val, regTok.Decimals),
				Symbol:        regTok.Label,
				TokenContract: regTok.Contract,
				TokenTier:     regTok.Tier,
				Counterparty:  fromAddr,
				Block:         blk,
				Flags:         []string{},
				rawValue:      val.String(),
			})
		}
		if segEnd == toBlock {
			break
		}
		cur = segEnd + 1
	}
	sortPayTxsDesc(out)
	return out, nil
}

type tronTRC20Row struct {
	TransactionID  string `json:"transaction_id"`
	BlockTimestamp int64  `json:"block_timestamp"`
	From           string `json:"from"`
	To             string `json:"to"`
	Type           string `json:"type"`
	Value          string `json:"value"`
	TokenInfo      struct {
		Symbol   string `json:"symbol"`
		Address  string `json:"address"`
		Decimals int    `json:"decimals"`
	} `json:"token_info"`
}

type tronTxRow struct {
	TxID           string `json:"txID"`
	BlockNumber    uint64 `json:"blockNumber"`
	BlockTimestamp int64  `json:"block_timestamp"`
	Ret            []struct {
		ContractRet string `json:"contractRet"`
	} `json:"ret"`
	RawData struct {
		Contract []struct {
			Type      string `json:"type"`
			Parameter struct {
				Value struct {
					Amount          int64  `json:"amount"`
					OwnerAddress    string `json:"owner_address"`
					ToAddress       string `json:"to_address"`
					ContractAddress string `json:"contract_address"`
					Data            string `json:"data"`
				} `json:"value"`
			} `json:"parameter"`
		} `json:"contract"`
	} `json:"raw_data"`
}

func normalizeTronAddr(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	if len(s) == 34 && s[0] == 'T' {
		return s, true
	}
	b58, err := tronHexToBase58(s)
	if err != nil {
		return "", false
	}
	return b58, true
}

func fetchTronTRC20Page(ctx context.Context, addr, contract string, minTS int64, limit int, fingerprint string) ([]tronTRC20Row, string, string) {
	q := url.Values{
		"limit": {strconv.Itoa(limit)},
	}
	if contract != "" {
		q.Set("contract_address", contract)
	}
	if minTS > 0 {
		q.Set("min_timestamp", strconv.FormatInt(minTS, 10))
	}
	if fingerprint != "" {
		q.Set("fingerprint", fingerprint)
	}
	path := "/v1/accounts/" + url.PathEscape(addr) + "/transactions/trc20?" + q.Encode()
	raw, code := tronGridDo(ctx, http.MethodGet, path, nil)
	if code != "" {
		return nil, "", code
	}
	var parsed struct {
		Data []tronTRC20Row `json:"data"`
		Meta struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, "", "bad_response"
	}
	return parsed.Data, parsed.Meta.Fingerprint, ""
}

func fetchTronTransactionsPage(ctx context.Context, addr string, onlyFrom bool, minTS int64, limit int, fingerprint string) ([]tronTxRow, string, string) {
	q := url.Values{
		"limit": {strconv.Itoa(limit)},
	}
	if onlyFrom {
		q.Set("only_from", "true")
	}
	if minTS > 0 {
		q.Set("min_timestamp", strconv.FormatInt(minTS, 10))
	}
	if fingerprint != "" {
		q.Set("fingerprint", fingerprint)
	}
	path := "/v1/accounts/" + url.PathEscape(addr) + "/transactions?" + q.Encode()
	raw, code := tronGridDo(ctx, http.MethodGet, path, nil)
	if code != "" {
		return nil, "", code
	}
	var parsed struct {
		Data []tronTxRow `json:"data"`
		Meta struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, "", "bad_response"
	}
	return parsed.Data, parsed.Meta.Fingerprint, ""
}

func convertTronTRC20Row(r tronTRC20Row, asset, addr string, onlyNonRegistry bool) (payTx, bool) {
	from, ok1 := normalizeTronAddr(r.From)
	to, ok2 := normalizeTronAddr(r.To)
	contract, ok3 := normalizeTronAddr(r.TokenInfo.Address)
	if !ok1 || !ok2 || !ok3 {
		return payTx{}, false
	}
	regTok, isReg := lookupRegistryToken("tron", contract)
	if onlyNonRegistry && isReg {
		return payTx{}, false
	}

	var dir, cp string
	switch {
	case from == addr:
		dir, cp = "out", to
	case to == addr:
		dir, cp = "in", from
	default:
		return payTx{}, false
	}

	v, ok := new(big.Int).SetString(strings.TrimSpace(r.Value), 10)
	if !ok {
		return payTx{}, false
	}

	var (
		tier     tokenTier
		symbol   string
		decimals int
	)
	if isReg {
		decimals = regTok.Decimals
		symbol = regTok.Label
		if regTok.Asset == asset {
			tier = regTok.Tier
		} else {
			tier = tierOther
		}
	} else {
		decimals = r.TokenInfo.Decimals
		if decimals <= 0 || decimals > 36 {
			decimals = 6
		}
		upSym := strings.TrimSpace(r.TokenInfo.Symbol)
		if normalizeMimicSymbol(upSym) == asset {
			tier = tierCounterfeit
			symbol = fmt.Sprintf("%q", upSym)
		} else {
			tier = tierOther
			symbol = upSym
		}
	}

	tsSec := r.BlockTimestamp / 1000
	return payTx{
		TxHash:        r.TransactionID,
		ExplorerURL:   fmt.Sprintf(chains["tron"].TxURL, r.TransactionID),
		Direction:     dir,
		Timestamp:     time.Unix(tsSec, 0).UTC().Format(time.RFC3339),
		Amount:        formatUnits(v, decimals),
		Symbol:        symbol,
		TokenContract: contract,
		TokenTier:     tier,
		Counterparty:  cp,
		Block:         0,
		Flags:         []string{},
		rawValue:      v.String(),
	}, true
}

func convertTronAccountTxRows(rows []tronTxRow, asset, addr string) []payTx {
	info := chains["tron"]
	var out []payTx
	for _, r := range rows {
		if len(r.RawData.Contract) == 0 {
			continue
		}
		c := r.RawData.Contract[0]
		failed := len(r.Ret) > 0 && r.Ret[0].ContractRet != "" && r.Ret[0].ContractRet != "SUCCESS"
		tsSec := r.BlockTimestamp / 1000
		tsStr := time.Unix(tsSec, 0).UTC().Format(time.RFC3339)

		switch {
		case asset == "TRX" && c.Type == "TransferContract":
			owner, ok1 := normalizeTronAddr(c.Parameter.Value.OwnerAddress)
			to, ok2 := normalizeTronAddr(c.Parameter.Value.ToAddress)
			if !ok1 || !ok2 || c.Parameter.Value.Amount < 0 {
				continue
			}
			var dir, cp string
			switch {
			case owner == addr:
				dir, cp = "out", to
			case to == addr:
				if failed {
					continue
				}
				dir, cp = "in", owner
			default:
				continue
			}
			amt := big.NewInt(c.Parameter.Value.Amount)
			out = append(out, payTx{
				TxHash:       r.TxID,
				ExplorerURL:  fmt.Sprintf(info.TxURL, r.TxID),
				Direction:    dir,
				Timestamp:    tsStr,
				Amount:       formatUnits(amt, 6),
				Symbol:       "TRX",
				TokenTier:    tierNative,
				Counterparty: cp,
				Block:        r.BlockNumber,
				Failed:       failed,
				Flags:        []string{},
				rawValue:     amt.String(),
			})

		case asset != "TRX" && c.Type == "TriggerSmartContract" && failed:
			owner, okOwner := normalizeTronAddr(c.Parameter.Value.OwnerAddress)
			if !okOwner || owner != addr {
				continue
			}
			contractB58, okContract := normalizeTronAddr(c.Parameter.Value.ContractAddress)
			if !okContract {
				continue
			}
			regTok, isReg := lookupRegistryToken("tron", contractB58)
			if !isReg || regTok.Asset != asset {
				continue
			}
			to20, amt, okCall := decodeTransferCalldata(c.Parameter.Value.Data)
			if !okCall {
				continue
			}
			recipientB58, err := tronHexToBase58(to20)
			if err != nil {
				continue
			}
			out = append(out, payTx{
				TxHash:        r.TxID,
				ExplorerURL:   fmt.Sprintf(info.TxURL, r.TxID),
				Direction:     "out",
				Timestamp:     tsStr,
				Amount:        formatUnits(amt, regTok.Decimals),
				Symbol:        regTok.Label,
				TokenContract: regTok.Contract,
				TokenTier:     regTok.Tier,
				Counterparty:  recipientB58,
				Block:         r.BlockNumber,
				Failed:        true,
				Flags:         []string{},
				rawValue:      amt.String(),
			})
		}
	}
	return out
}

func fetchTronTxBlockNumber(ctx context.Context, txID string) uint64 {
	payload, err := json.Marshal(map[string]any{"value": txID})
	if err != nil {
		return 0
	}
	reqCtx, cancel := context.WithTimeout(ctx, payUpstreamTimeout)
	defer cancel()
	raw, code := tronGridDo(reqCtx, http.MethodPost, "/wallet/gettransactioninfobyid", payload)
	if code != "" {
		return 0
	}
	var parsed struct {
		BlockNumber uint64 `json:"blockNumber"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return 0
	}
	return parsed.BlockNumber
}

// fetchTronHistory fetches the 7-day transaction history for USDT or TRX on
// TRON via TronGrid, returning raw rows and scope metadata.
func fetchTronHistory(ctx context.Context, asset, addr string, since time.Time) ([]payTx, payHistoryScope, error) {
	ctx, cancel := context.WithTimeout(ctx, payTronHistoryTimeout)
	defer cancel()
	minTS := since.UnixMilli()
	scope := payHistoryScope{Hosts: hostsOf(tronGridBaseURL)}

	if asset == "TRX" {
		var allRows []tronTxRow
		fp := ""
		for len(allRows) < payHistoryMaxRows {
			page, nextFP, code := fetchTronTransactionsPage(ctx, addr, false, minTS, tronPageSize, fp)
			if code != "" {
				return nil, scope, errors.New(code)
			}
			allRows = append(allRows, page...)
			if nextFP == "" || len(page) == 0 {
				break
			}
			if len(allRows) >= payHistoryMaxRows {
				scope.Truncated = true
				allRows = allRows[:payHistoryMaxRows]
				break
			}
			fp = nextFP
		}
		scope.Transactions = len(allRows)
		txs := convertTronAccountTxRows(allRows, "TRX", addr)
		sortPayTxsDesc(txs)
		return txs, scope, nil
	}

	var (
		regTxs         []payTx
		regCount       int
		regTruncated   bool
		regCode        string
		unfilteredRows []tronTRC20Row
		unfilteredCode string
		outRows        []tronTxRow
		outCode        string
		solidTime      int64
		wg             sync.WaitGroup
	)

	wg.Add(4)
	go func() {
		defer wg.Done()
		for _, tok := range tokensFor(asset, "tron") {
			var regRows []tronTRC20Row
			fp := ""
			for len(regRows) < payHistoryMaxRows {
				page, nextFP, code := fetchTronTRC20Page(ctx, addr, tok.Contract, minTS, tronPageSize, fp)
				if code != "" {
					regCode = code
					return
				}
				regRows = append(regRows, page...)
				if nextFP == "" || len(page) == 0 {
					break
				}
				if len(regRows) >= payHistoryMaxRows {
					regTruncated = true
					regRows = regRows[:payHistoryMaxRows]
					break
				}
				fp = nextFP
			}
			regCount += len(regRows)
			for _, r := range regRows {
				if tx, ok := convertTronTRC20Row(r, asset, addr, false); ok {
					regTxs = append(regTxs, tx)
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		// Single unfiltered page (200 rows) to surface counterfeit USDT transfers;
		// keep only non-registry contracts so official USDT rows are not duplicated (review P2-1).
		unfilteredRows, _, unfilteredCode = fetchTronTRC20Page(ctx, addr, "", minTS, tronPageSize, "")
	}()
	go func() {
		defer wg.Done()
		// Single page of outgoing account transactions to catch failed TRC-20 transfers
		// (e.g. OUT_OF_ENERGY), which emit no Transfer event (review P1-10, review-2 #4).
		outRows, _, outCode = fetchTronTransactionsPage(ctx, addr, true, minTS, tronPageSize, "")
	}()
	go func() {
		defer wg.Done()
		solidCtx, solidCancel := context.WithTimeout(ctx, payUpstreamTimeout)
		defer solidCancel()
		_, solidTime, _ = fetchTronBlockOnce(solidCtx, "/walletsolidity/getnowblock")
	}()
	wg.Wait()

	if regCode != "" {
		return nil, scope, errors.New(regCode)
	}
	if unfilteredCode != "" {
		return nil, scope, errors.New(unfilteredCode)
	}
	if outCode != "" {
		return nil, scope, errors.New(outCode)
	}

	txs := regTxs
	scope.TRC20 = regCount
	scope.Truncated = regTruncated
	for _, r := range unfilteredRows {
		if tx, ok := convertTronTRC20Row(r, asset, addr, true); ok {
			txs = append(txs, tx)
			scope.TRC20++
		}
	}

	scope.Transactions = len(outRows)
	if len(outRows) >= tronPageSize {
		scope.TruncatedFailedOutgoing = true
	}
	txs = append(txs, convertTronAccountTxRows(outRows, asset, addr)...)
	sortPayTxsDesc(txs)

	// Resolve block numbers for at most the 5 newest unsolidified TRC-20 rows (review-2 #4).
	var lookupIndices []int
	for i := range txs {
		if len(lookupIndices) >= tronMaxTxInfoLookups {
			break
		}
		if txs[i].Block != 0 || txs[i].Failed {
			continue
		}
		txTime, err := time.Parse(time.RFC3339, txs[i].Timestamp)
		if err != nil {
			continue
		}
		if solidTime > 0 && txTime.Unix() <= solidTime {
			continue
		}
		lookupIndices = append(lookupIndices, i)
	}

	var txInfoWG sync.WaitGroup
	for _, idx := range lookupIndices {
		txInfoWG.Add(1)
		go func(i int) {
			defer txInfoWG.Done()
			if blk := fetchTronTxBlockNumber(ctx, txs[i].TxHash); blk > 0 {
				txs[i].Block = blk
			}
		}(idx)
	}
	txInfoWG.Wait()

	return txs, scope, nil
}

// fetchTronRecent fetches the newest 20 rows (page 1 only) for the lightweight
// recent-transactions check in /live (spec §5.6).
func fetchTronRecent(ctx context.Context, asset, addr string) ([]payTx, error) {
	if asset == "TRX" {
		rows, _, code := fetchTronTransactionsPage(ctx, addr, false, 0, payRecentPageSize, "")
		if code != "" {
			return nil, errors.New(code)
		}
		txs := convertTronAccountTxRows(rows, "TRX", addr)
		sortPayTxsDesc(txs)
		return txs, nil
	}

	var txs []payTx
	for _, tok := range tokensFor(asset, "tron") {
		rows, _, code := fetchTronTRC20Page(ctx, addr, tok.Contract, 0, payRecentPageSize, "")
		if code != "" {
			return nil, errors.New(code)
		}
		for _, r := range rows {
			if tx, ok := convertTronTRC20Row(r, asset, addr, false); ok {
				txs = append(txs, tx)
			}
		}
	}
	sortPayTxsDesc(txs)
	return txs, nil
}

func canonicalRawValue(tx payTx) string {
	s := strings.TrimSpace(tx.rawValue)
	if s == "" {
		s = strings.TrimSpace(tx.Amount)
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		if n, ok := parseHexBigInt(s); ok {
			return n.String()
		}
	}
	if n, ok := new(big.Int).SetString(s, 10); ok {
		return n.String()
	}
	return s
}

func normalizeDedupAddr(a string) string {
	s := strings.TrimSpace(a)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return strings.ToLower(s)
	}
	return s
}

// mergeTxs deduplicates transactions across sources using the 6-tuple key
// (tx_hash, contract, direction, counterparty, raw_value, occ) where occ is the
// 0-based occurrence index of the 5-tuple within the same source list
// (spec Task 8, review-2 #3). Earlier lists (RPC unsettled logs) take
// precedence while missing timestamps or block numbers are backfilled.
func mergeTxs(lists ...[]payTx) []payTx {
	type tuple5 struct {
		hash     string
		contract string
		dir      string
		cp       string
		rawVal   string
	}
	type tuple6 struct {
		tuple5
		occ int
	}

	var out []payTx
	indexByKey := make(map[tuple6]int)

	for _, list := range lists {
		occCount := make(map[tuple5]int)
		for _, tx := range list {
			t5 := tuple5{
				hash:     strings.ToLower(strings.TrimSpace(tx.TxHash)),
				contract: normalizeDedupAddr(tx.TokenContract),
				dir:      tx.Direction,
				cp:       normalizeDedupAddr(tx.Counterparty),
				rawVal:   canonicalRawValue(tx),
			}
			occ := occCount[t5]
			occCount[t5] = occ + 1
			k := tuple6{tuple5: t5, occ: occ}

			if idx, exists := indexByKey[k]; exists {
				if out[idx].Block == 0 && tx.Block > 0 {
					out[idx].Block = tx.Block
				}
				if out[idx].Timestamp == "" && tx.Timestamp != "" {
					out[idx].Timestamp = tx.Timestamp
				}
				if !out[idx].Failed && tx.Failed {
					out[idx].Failed = true
				}
				continue
			}
			indexByKey[k] = len(out)
			out = append(out, tx)
		}
	}
	sortPayTxsDesc(out)
	return out
}
