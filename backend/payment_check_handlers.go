package main

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	payHeadsCacheTTL    = 4 * time.Second
	payRecentCacheTTL   = 4 * time.Second
	payHistoryCacheTTL  = 60 * time.Second
	payLiveTotalTimeout = 15 * time.Second
	payAlertWindow      = 24 * time.Hour
)

var payLagSamplers = map[string]*lagSampler{
	"eth":  {},
	"arb":  {},
	"op":   {},
	"base": {},
	"tron": {},
	"btc":  {},
	"sol":  {},
}

type payLiveResponse struct {
	Asset        string          `json:"asset"`
	Network      string          `json:"network"`
	NetworkLabel string          `json:"network_label"`
	Address      string          `json:"address"`
	AsOf         string          `json:"as_of"`
	Heads        payHeads        `json:"heads"`
	Balance      []payBalanceRow `json:"balance"`
	BalanceError string          `json:"balance_error,omitempty"`
	Latest       *payTx          `json:"latest"`
	Alerts       []payAlert      `json:"alerts"`
	Sources      []riskSource    `json:"sources"`
}

type payHistoryResponse struct {
	Asset   string          `json:"asset"`
	Network string          `json:"network"`
	Address string          `json:"address"`
	AsOf    string          `json:"as_of"`
	Since   string          `json:"since"`
	Txs     []payTx         `json:"txs"`
	Scope   payHistoryScope `json:"scope"`
	Sources []riskSource    `json:"sources"`
}

type payAlert struct {
	Severity string `json:"severity"` // critical | warning
	Code     string `json:"code"`     // own_address_frozen | own_address_sanctioned | counterfeit_received | poisoning_received
	Message  string `json:"message"`
	TxHash   string `json:"tx_hash,omitempty"`
}

type cachedPayHistory struct {
	txs   []payTx
	scope payHistoryScope
	since string
}

// rpcsFor returns the configured RPC endpoint list for network from l2Chains
// (populated at startup by applyCryptoGasConfig; spec Task 10).
func rpcsFor(network string) []string {
	for i := range l2Chains {
		if l2Chains[i].ID == network {
			return l2Chains[i].RPCs
		}
	}
	return nil
}

func (h *APIHandler) payRPCs(network string) []string {
	if h != nil && h.risk != nil && len(h.risk.chainRPCs[network]) > 0 {
		return h.risk.chainRPCs[network]
	}
	return rpcsFor(network)
}

func (h *APIHandler) payBlockscoutURL(network string) string {
	if h != nil && h.risk != nil {
		return h.risk.blockscoutURL(network)
	}
	return (PaymentCheckConfig{}).blockscoutURL(network)
}

func (h *APIHandler) payRiskStore() *riskStore {
	if h != nil && h.risk != nil {
		return h.risk.store
	}
	return nil
}

func clonePayTxs(src []payTx) []payTx {
	if len(src) == 0 {
		return []payTx{}
	}
	out := make([]payTx, len(src))
	for i := range src {
		out[i] = src[i]
		out[i].Flags = slices.Clone(src[i].Flags)
		if out[i].Flags == nil {
			out[i].Flags = []string{}
		}
		out[i].CounterpartyHits = slices.Clone(src[i].CounterpartyHits)
	}
	return out
}

func (h *APIHandler) fetchAndSampleHeads(ctx context.Context, network string) (payHeads, error) {
	var (
		heads payHeads
		err   error
	)
	switch network {
	case "btc":
		heads, err = fetchBTCHeads(ctx)
	case "sol":
		heads, err = fetchSolanaHeads(ctx, h.payRPCs("sol"))
	case "tron":
		heads, err = fetchTronHeads(ctx)
	default:
		heads, err = fetchEVMHeads(ctx, h.payRPCs(network))
	}
	if err != nil {
		return payHeads{}, err
	}
	if sampler := payLagSamplers[network]; sampler != nil {
		heads.SafeLagSec, heads.FinalLagSec = sampler.observe(time.Now(), heads.SafeLagSec, heads.FinalLagSec)
	}
	if h.cache != nil {
		h.cache.SetWithTTL("pay:heads:"+network, heads, payHeadsCacheTTL)
	}
	return heads, nil
}

func (h *APIHandler) getPayHeads(ctx context.Context, network string) (payHeads, error) {
	key := "pay:heads:" + network
	if h.cache != nil {
		if cached, ok := h.cache.Get(key); ok {
			return cached.(payHeads), nil
		}
		v, err, _ := h.sf.Do(key, func() (any, error) {
			if cached, ok := h.cache.Get(key); ok {
				return cached, nil
			}
			return h.fetchAndSampleHeads(ctx, network)
		})
		if err != nil {
			return payHeads{}, err
		}
		return v.(payHeads), nil
	}
	return h.fetchAndSampleHeads(ctx, network)
}

func (h *APIHandler) getPayRecent(ctx context.Context, blockscoutURL, asset, network, addr string, heads payHeads) ([]payTx, error) {
	key := "pay:recent:" + network + ":" + asset + ":" + addr
	if h.cache != nil {
		if cached, ok := h.cache.Get(key); ok {
			return clonePayTxs(cached.([]payTx)), nil
		}
	}
	v, err, _ := h.sf.Do(key, func() (any, error) {
		if h.cache != nil {
			if cached, ok := h.cache.Get(key); ok {
				return cached, nil
			}
		}
		var (
			txs []payTx
			err error
		)
		switch network {
		case "btc":
			txs, err = fetchBTCRecent(ctx, addr)
		case "sol":
			txs, err = fetchSolanaRecent(ctx, h.cache, h.payRPCs("sol"), asset, addr, heads)
		case "tron":
			txs, err = fetchTronRecent(ctx, asset, addr)
		default:
			txs, err = fetchEVMRecent(ctx, blockscoutURL, asset, network, addr)
		}
		if err != nil {
			return nil, err
		}
		if h.cache != nil && len(txs) > 0 {
			h.cache.SetWithTTL(key, clonePayTxs(txs), payRecentCacheTTL)
		}
		return txs, nil
	})
	if err != nil {
		return nil, err
	}
	return clonePayTxs(v.([]payTx)), nil
}

func classifyPayTxs(txs []payTx, network string, heads payHeads, nowUnix int64) {
	for i := range txs {
		var blockTime int64
		if txs[i].Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, txs[i].Timestamp); err == nil {
				blockTime = t.Unix()
			}
		}
		switch network {
		case "btc":
			txs[i].Level, txs[i].Progress, txs[i].EstSecLeft = classifyBTC(txs[i].Block, heads)
		case "sol":
			txs[i].Level, txs[i].Progress, txs[i].EstSecLeft = classifySolana(txs[i].Block, blockTime, txs[i].Failed, heads, nowUnix)
		case "tron":
			txs[i].Level, txs[i].Progress, txs[i].EstSecLeft = classifyTron(txs[i].Block, blockTime, txs[i].Failed, heads)
		default:
			txs[i].Level, txs[i].Progress, txs[i].EstSecLeft = classifyEVM(txs[i].Block, blockTime, txs[i].Failed, heads, nowUnix)
		}
	}
}

func hasAssetFreezeSupport(asset, network string) bool {
	for _, tok := range tokensFor(asset, network) {
		if tok.FreezeSel != "" {
			return true
		}
	}
	return false
}

func isWithinAlertWindow(ts string, now time.Time) bool {
	if ts == "" {
		// Unsettled RPC log in (finalized-50, latest] or 0-conf BTC mempool tx without timestamp is fresh.
		return true
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return false
	}
	return now.Sub(t) <= payAlertWindow
}

func parsePaymentCheckParams(w http.ResponseWriter, r *http.Request) (asset, network, addr string, ok bool) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return "", "", "", false
	}
	asset = strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("asset")))
	network = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("network")))
	if !validPair(asset, network) {
		writeError(w, "unsupported asset/network", http.StatusBadRequest)
		return "", "", "", false
	}
	parsedAddr, _, err := parseChainAddress(network, r.URL.Query().Get("address"))
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return "", "", "", false
	}
	return asset, network, parsedAddr, true
}

func (h *APIHandler) PaymentCheckLive(w http.ResponseWriter, r *http.Request) {
	asset, network, addr, ok := parsePaymentCheckParams(w, r)
	if !ok {
		return
	}

	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), payLiveTotalTimeout)
	defer cancel()

	now := time.Now().UTC()
	rpcs := h.payRPCs(network)
	bsURL := h.payBlockscoutURL(network)
	store := h.payRiskStore()

	heads, headsErr := h.getPayHeads(bgCtx, network)

	var (
		balanceRows   []payBalanceRow
		unsettledTxs  []payTx
		unsettledErr  error
		recentTxs     []payTx
		recentErr     error
		histTxs       []payTx
		freezeStates  []issuerFreezeState
		freezeErrCode string
		ownOFACHit    bool
		localErr      error
	)

	checkFreeze := hasAssetFreezeSupport(asset, network)
	g, ctx := errgroup.WithContext(bgCtx)

	// 1. Balance (bounded by payUpstreamTimeout so 9 calls at concurrency 4 cannot drag /live)
	g.Go(func() error {
		balCtx, balCancel := context.WithTimeout(ctx, payUpstreamTimeout)
		defer balCancel()
		switch network {
		case "btc":
			balanceRows = fetchBTCBalances(balCtx, addr, heads)
		case "sol":
			balanceRows = fetchSolanaBalances(balCtx, rpcs, asset, addr)
		case "tron":
			balanceRows = fetchTronBalances(balCtx, asset, addr)
		default:
			balanceRows = fetchEVMBalances(balCtx, rpcs, asset, network, addr)
		}
		return nil
	})

	// 2. Unsettled logs (EVM stablecoins) OR lightweight recent txs (BTC, Solana, TRON, EVM native ETH)
	g.Go(func() error {
		switch {
		case network == "btc" || network == "sol" || network == "tron":
			recentTxs, recentErr = h.getPayRecent(ctx, "", asset, network, addr, heads)
		case asset == "ETH":
			recentTxs, recentErr = h.getPayRecent(ctx, bsURL, asset, network, addr, heads)
		default:
			if heads.Latest > 0 {
				fromBlock := uint64(1)
				if heads.Finalized > 50 {
					fromBlock = heads.Finalized - 50
				}
				unsettledTxs, unsettledErr = fetchEVMUnsettled(ctx, rpcs, asset, network, addr, fromBlock, heads.Latest)
			}
		}
		return nil
	})

	// 3. Non-blocking read of 7-day history cache (never fetches history synchronously)
	g.Go(func() error {
		if h.cache != nil {
			histKey := "pay:hist:" + network + ":" + asset + ":" + addr
			if cached, ok := h.cache.Get(histKey); ok {
				if entry, ok := cached.(*cachedPayHistory); ok {
					histTxs = clonePayTxs(entry.txs)
				}
			}
		}
		return nil
	})

	// 4. Own address issuer freeze check
	if checkFreeze {
		g.Go(func() error {
			freezeStates, freezeErrCode = checkIssuerFreeze(ctx, network, addr, asset, rpcs)
			return nil
		})
	}

	// 5. Own address local OFAC check
	g.Go(func() error {
		if store == nil {
			localErr = fmt.Errorf("unavailable")
			return nil
		}
		hits, err := store.listHits(ctx, addr)
		if err != nil {
			localErr = fmt.Errorf("unavailable")
			return nil
		}
		for _, hit := range hits {
			if hit.Source == "ofac" {
				ownOFACHit = true
				break
			}
		}
		return nil
	})

	_ = g.Wait()

	candidates := mergeTxs(unsettledTxs, recentTxs, histTxs)
	classifyPayTxs(candidates, network, heads, now.Unix())
	if len(candidates) > 0 {
		if err := applyLocalHits(bgCtx, store, network, candidates); err != nil {
			localErr = fmt.Errorf("unavailable")
		}
	}
	applyFlags(candidates, asset, chains[network].Family)

	var latest *payTx
	for i := range candidates {
		tx := &candidates[i]
		if tx.Direction != "in" || tx.Failed {
			continue
		}
		if tx.TokenTier != tierNative && tx.TokenTier != tierBridged {
			continue
		}
		if slices.Contains(tx.Flags, "zero_value") || slices.Contains(tx.Flags, "dust") {
			continue
		}
		cpTx := *tx
		latest = &cpTx
		break
	}

	alerts := []payAlert{}
	for _, st := range freezeStates {
		if st.Frozen {
			alerts = append(alerts, payAlert{
				Severity: "critical",
				Code:     "own_address_frozen",
				Message:  fmt.Sprintf("This address is frozen by the %s contract — received %s cannot be moved.", st.Token, asset),
			})
		}
	}
	if ownOFACHit {
		alerts = append(alerts, payAlert{
			Severity: "critical",
			Code:     "own_address_sanctioned",
			Message:  "This address appears on the OFAC SDN sanctions list.",
		})
	}
	type txAlertCand struct {
		prio  int
		alert payAlert
	}
	var txAlertOrder []string
	txAlerts := make(map[string]txAlertCand)
	for _, tx := range candidates {
		if !isWithinAlertWindow(tx.Timestamp, now) {
			continue
		}
		var cand txAlertCand
		switch {
		case slices.Contains(tx.Flags, "counterfeit_token"):
			cand = txAlertCand{
				prio: 3,
				alert: payAlert{
					Severity: "critical",
					Code:     "counterfeit_received",
					Message:  fmt.Sprintf("Counterfeit %s transfer (%s %s) detected in the last 24h — not genuine %s.", asset, tx.Amount, tx.Symbol, asset),
					TxHash:   tx.TxHash,
				},
			}
		case slices.Contains(tx.Flags, "sent_to_lookalike"):
			cand = txAlertCand{
				prio: 2,
				alert: payAlert{
					Severity: "critical",
					Code:     "sent_to_lookalike",
					Message:  fmt.Sprintf("Outgoing transfer (%s %s) was sent to a lookalike address (%s) matching an earlier counterparty.", tx.Amount, tx.Symbol, tx.Counterparty),
					TxHash:   tx.TxHash,
				},
			}
		case slices.Contains(tx.Flags, "lookalike") || slices.Contains(tx.Flags, "lookalike_known"):
			cand = txAlertCand{
				prio: 1,
				alert: payAlert{
					Severity: "warning",
					Code:     "poisoning_received",
					Message:  fmt.Sprintf("Address poisoning attempt (%s %s) involving lookalike counterparty %s in the last 24h.", tx.Amount, tx.Symbol, tx.Counterparty),
					TxHash:   tx.TxHash,
				},
			}
		default:
			continue
		}
		existing, seen := txAlerts[tx.TxHash]
		if !seen {
			txAlertOrder = append(txAlertOrder, tx.TxHash)
			txAlerts[tx.TxHash] = cand
		} else if cand.prio > existing.prio {
			txAlerts[tx.TxHash] = cand
		}
	}
	for _, h := range txAlertOrder {
		alerts = append(alerts, txAlerts[h].alert)
	}

	var sources []riskSource
	switch network {
	case "btc":
		btcHosts := hostsOf(esploraAPIBases()...)
		src := riskSource{ID: "mempool", Status: "ok", Hosts: btcHosts}
		if headsErr != nil {
			src.Status, src.Error = "error", headsErr.Error()
		} else if recentErr != nil {
			src.Status, src.Error = "error", recentErr.Error()
		}
		sources = append(sources, src)
	case "sol":
		solHosts := hostsOf(rpcs...)
		src := riskSource{ID: "solana_rpc", Status: "ok", Hosts: solHosts}
		if headsErr != nil {
			src.Status, src.Error = "error", headsErr.Error()
		} else if recentErr != nil {
			src.Status, src.Error = "error", recentErr.Error()
		}
		sources = append(sources, src)
		if checkFreeze {
			fSrc := riskSource{ID: "issuer_freeze", Status: "ok", Hosts: solHosts}
			if freezeErrCode != "" {
				fSrc.Status, fSrc.Error = "error", freezeErrCode
			}
			sources = append(sources, fSrc)
		}
	case "tron":
		tronHosts := hostsOf(tronGridBaseURL)
		src := riskSource{ID: "trongrid", Status: "ok", Hosts: tronHosts}
		if headsErr != nil {
			src.Status, src.Error = "error", headsErr.Error()
		} else if recentErr != nil {
			src.Status, src.Error = "error", recentErr.Error()
		}
		sources = append(sources, src)
		if checkFreeze {
			fSrc := riskSource{ID: "issuer_freeze", Status: "ok", Hosts: tronHosts}
			if freezeErrCode != "" {
				fSrc.Status, fSrc.Error = "error", freezeErrCode
			}
			sources = append(sources, fSrc)
		}
	default:
		rpcHosts := hostsOf(rpcs...)
		src := riskSource{ID: "rpc", Status: "ok", Hosts: rpcHosts}
		if headsErr != nil {
			src.Status, src.Error = "error", headsErr.Error()
		} else if unsettledErr != nil {
			src.Status, src.Error = "error", unsettledErr.Error()
		}
		sources = append(sources, src)
		if asset == "ETH" {
			bSrc := riskSource{ID: "blockscout", Status: "ok", Hosts: hostsOf(bsURL)}
			if recentErr != nil {
				bSrc.Status, bSrc.Error = "error", recentErr.Error()
			}
			sources = append(sources, bSrc)
		}
		if checkFreeze {
			fSrc := riskSource{ID: "issuer_freeze", Status: "ok", Hosts: rpcHosts}
			if freezeErrCode != "" {
				fSrc.Status, fSrc.Error = "error", freezeErrCode
			}
			sources = append(sources, fSrc)
		}
	}

	localSrc := riskSource{ID: "local", Status: "ok"}
	if localErr != nil {
		localSrc.Status, localSrc.Error = "error", "unavailable"
	}
	sources = append(sources, localSrc)

	if balanceRows == nil {
		balanceRows = []payBalanceRow{}
	}
	var balErrStr string
	if len(balanceRows) > 0 {
		allFailed := true
		for _, r := range balanceRows {
			if r.Error == "" {
				allFailed = false
				break
			}
		}
		if allFailed {
			balErrStr = balanceRows[0].Error
		}
	}

	writeJSON(w, payLiveResponse{
		Asset:        asset,
		Network:      network,
		NetworkLabel: chains[network].Label,
		Address:      addr,
		AsOf:         now.Format(time.RFC3339),
		Heads:        heads,
		Balance:      balanceRows,
		BalanceError: balErrStr,
		Latest:       latest,
		Alerts:       alerts,
		Sources:      sources,
	})
}

func (h *APIHandler) PaymentCheckHistory(w http.ResponseWriter, r *http.Request) {
	asset, network, addr, ok := parsePaymentCheckParams(w, r)
	if !ok {
		return
	}

	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), payTronHistoryTimeout)
	defer cancel()

	now := time.Now().UTC()
	since := now.Add(-payHistoryWindow)
	bsURL := h.payBlockscoutURL(network)
	rpcs := h.payRPCs(network)
	store := h.payRiskStore()
	histKey := "pay:hist:" + network + ":" + asset + ":" + addr

	var (
		histEntry *cachedPayHistory
		histErr   error
		heads     payHeads
		headsErr  error
	)

	fetchHist := func() {
		if h.cache != nil {
			if cached, ok := h.cache.Get(histKey); ok {
				histEntry = cached.(*cachedPayHistory)
				return
			}
		}
		v, err, _ := h.sf.Do(histKey, func() (any, error) {
			if h.cache != nil {
				if cached, ok := h.cache.Get(histKey); ok {
					return cached, nil
				}
			}
			var (
				rawTxs []payTx
				scope  payHistoryScope
				err    error
			)
			switch network {
			case "btc":
				rawTxs, scope, err = fetchBTCHistory(bgCtx, addr, since)
			case "sol":
				rawTxs, scope, err = fetchSolanaHistory(bgCtx, h.cache, rpcs, asset, addr, since, heads)
			case "tron":
				rawTxs, scope, err = fetchTronHistory(bgCtx, asset, addr, since)
			default:
				rawTxs, scope, err = fetchEVMHistory(bgCtx, bsURL, asset, network, addr, since)
			}
			if err != nil {
				return &cachedPayHistory{
					txs:   []payTx{},
					scope: scope,
					since: since.Format(time.RFC3339),
				}, err
			}
			entry := &cachedPayHistory{
				txs:   clonePayTxs(rawTxs),
				scope: scope,
				since: since.Format(time.RFC3339),
			}
			if h.cache != nil {
				h.cache.SetWithTTL(histKey, entry, payHistoryCacheTTL)
			}
			return entry, nil
		})
		if v != nil {
			histEntry = v.(*cachedPayHistory)
		}
		histErr = err
	}

	if network == "sol" {
		// Solana fetches heads first so finalized slot cache TTL (S1) applies to getTransaction calls.
		heads, headsErr = h.getPayHeads(bgCtx, network)
		fetchHist()
	} else {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			fetchHist()
		}()
		go func() {
			defer wg.Done()
			heads, headsErr = h.getPayHeads(bgCtx, network)
		}()
		wg.Wait()
	}

	var (
		txs      []payTx
		scope    payHistoryScope
		sinceStr = since.Format(time.RFC3339)
	)
	if histEntry != nil {
		txs = clonePayTxs(histEntry.txs)
		scope = histEntry.scope
		if histEntry.since != "" {
			sinceStr = histEntry.since
		}
	} else {
		txs = []payTx{}
	}

	classifyPayTxs(txs, network, heads, now.Unix())
	localErr := applyLocalHits(bgCtx, store, network, txs)
	applyFlags(txs, asset, chains[network].Family)

	var sources []riskSource
	switch network {
	case "btc":
		src := riskSource{ID: "mempool", Status: "ok", Hosts: hostsOf(esploraAPIBases()...)}
		if histErr != nil {
			src.Status, src.Error = "error", histErr.Error()
		} else if headsErr != nil {
			src.Status, src.Error = "error", headsErr.Error()
		}
		sources = append(sources, src)
	case "sol":
		src := riskSource{ID: "solana_rpc", Status: "ok", Hosts: hostsOf(rpcs...)}
		if histErr != nil {
			src.Status, src.Error = "error", histErr.Error()
		} else if headsErr != nil {
			src.Status, src.Error = "error", headsErr.Error()
		}
		sources = append(sources, src)
	case "tron":
		src := riskSource{ID: "trongrid", Status: "ok", Hosts: hostsOf(tronGridBaseURL)}
		if histErr != nil {
			src.Status, src.Error = "error", histErr.Error()
		} else if headsErr != nil {
			src.Status, src.Error = "error", headsErr.Error()
		}
		sources = append(sources, src)
	default:
		bSrc := riskSource{ID: "blockscout", Status: "ok", Hosts: hostsOf(bsURL)}
		if histErr != nil {
			bSrc.Status, bSrc.Error = "error", histErr.Error()
		}
		sources = append(sources, bSrc)

		rSrc := riskSource{ID: "rpc", Status: "ok", Hosts: hostsOf(rpcs...)}
		if headsErr != nil {
			rSrc.Status, rSrc.Error = "error", headsErr.Error()
		}
		sources = append(sources, rSrc)
	}

	localSrc := riskSource{ID: "local", Status: "ok"}
	if localErr != nil {
		localSrc.Status, localSrc.Error = "error", "unavailable"
	}
	sources = append(sources, localSrc)

	writeJSON(w, payHistoryResponse{
		Asset:   asset,
		Network: network,
		Address: addr,
		AsOf:    now.Format(time.RFC3339),
		Since:   sinceStr,
		Txs:     txs,
		Scope:   scope,
		Sources: sources,
	})
}
