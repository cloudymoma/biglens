package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"time"
)

const payBTCHistoryMaxPages = 8

type esploraTxStatus struct {
	Confirmed   bool   `json:"confirmed"`
	BlockHeight uint64 `json:"block_height"`
	BlockTime   int64  `json:"block_time"`
}

type esploraPrevout struct {
	ScriptpubkeyAddress string `json:"scriptpubkey_address"`
	Value               int64  `json:"value"`
}

type esploraVin struct {
	Sequence uint32          `json:"sequence"`
	Prevout  *esploraPrevout `json:"prevout"`
}

type esploraVout struct {
	ScriptpubkeyAddress string `json:"scriptpubkey_address"`
	Value               int64  `json:"value"`
}

type esploraTx struct {
	TxID   string          `json:"txid"`
	Status esploraTxStatus `json:"status"`
	Vin    []esploraVin    `json:"vin"`
	Vout   []esploraVout   `json:"vout"`
}

// convertBTCTx converts an Esplora UTXO transaction into one or more payTx rows
// relative to addr, setting TokenTier=tierNative (S5) and rbf_signaled when any
// input signals BIP-125 RBF (sequence < 0xfffffffe).
func convertBTCTx(tx esploraTx, addr string) []payTx {
	info := chains["btc"]
	explorerURL := fmt.Sprintf(info.TxURL, tx.TxID)

	var vinSelf, voutSelf int64
	rbf := false
	for _, in := range tx.Vin {
		if in.Sequence < 0xfffffffe {
			rbf = true
		}
		if in.Prevout != nil && in.Prevout.ScriptpubkeyAddress == addr {
			vinSelf += in.Prevout.Value
		}
	}
	for _, out := range tx.Vout {
		if out.ScriptpubkeyAddress == addr {
			voutSelf += out.Value
		}
	}

	var baseFlags []string
	if rbf {
		baseFlags = []string{"rbf_signaled"}
	} else {
		baseFlags = []string{}
	}

	var blk uint64
	var tsStr string
	if tx.Status.Confirmed {
		blk = tx.Status.BlockHeight
		if tx.Status.BlockTime > 0 {
			tsStr = time.Unix(tx.Status.BlockTime, 0).UTC().Format(time.RFC3339)
		}
	}

	// Pure incoming transfer: addr contributes no inputs and receives outputs.
	if vinSelf == 0 && voutSelf > 0 {
		var bestCP string
		var bestVal int64 = -1
		for _, in := range tx.Vin {
			if in.Prevout == nil || in.Prevout.ScriptpubkeyAddress == "" {
				continue
			}
			if in.Prevout.Value > bestVal {
				bestVal = in.Prevout.Value
				bestCP = in.Prevout.ScriptpubkeyAddress
			}
		}
		return []payTx{{
			TxHash:        tx.TxID,
			ExplorerURL:   explorerURL,
			Direction:     "in",
			Timestamp:     tsStr,
			Amount:        formatUnits(big.NewInt(voutSelf), 8),
			Symbol:        "BTC",
			TokenContract: "",
			TokenTier:     tierNative,
			Counterparty:  bestCP,
			Block:         blk,
			Flags:         slices.Clone(baseFlags),
			rawValue:      strconv.FormatInt(voutSelf, 10),
		}}
	}

	if vinSelf <= 0 {
		return nil
	}

	// Outgoing transfer: aggregate outputs per non-self recipient address,
	// preserving order of first appearance and dropping self change outputs.
	var recipients []string
	sums := make(map[string]int64)
	for _, out := range tx.Vout {
		rcpt := out.ScriptpubkeyAddress
		if rcpt == "" || rcpt == addr {
			continue
		}
		if _, seen := sums[rcpt]; !seen {
			recipients = append(recipients, rcpt)
		}
		sums[rcpt] += out.Value
	}

	if len(recipients) > 0 {
		rows := make([]payTx, 0, len(recipients))
		for _, rcpt := range recipients {
			amt := sums[rcpt]
			rows = append(rows, payTx{
				TxHash:        tx.TxID,
				ExplorerURL:   explorerURL,
				Direction:     "out",
				Timestamp:     tsStr,
				Amount:        formatUnits(big.NewInt(amt), 8),
				Symbol:        "BTC",
				TokenContract: "",
				TokenTier:     tierNative,
				Counterparty:  rcpt,
				Block:         blk,
				Flags:         slices.Clone(baseFlags),
				rawValue:      strconv.FormatInt(amt, 10),
			})
		}
		return rows
	}

	// Pure UTXO self-consolidation (all outputs return to addr).
	if voutSelf > 0 {
		return []payTx{{
			TxHash:        tx.TxID,
			ExplorerURL:   explorerURL,
			Direction:     "out",
			Timestamp:     tsStr,
			Amount:        formatUnits(big.NewInt(voutSelf), 8),
			Symbol:        "BTC",
			TokenContract: "",
			TokenTier:     tierNative,
			Counterparty:  addr,
			Block:         blk,
			Flags:         slices.Clone(baseFlags),
			rawValue:      strconv.FormatInt(voutSelf, 10),
		}}
	}
	return nil
}

// fetchBTCRecent fetches the first Esplora transaction page for /live.
func fetchBTCRecent(ctx context.Context, addr string) ([]payTx, error) {
	ctx, cancel := context.WithTimeout(ctx, payUpstreamTimeout)
	defer cancel()

	raw, code := esploraGet(ctx, "/address/"+addr+"/txs")
	if code != "" {
		return nil, errors.New(code)
	}
	var page []esploraTx
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, errors.New("bad_response")
	}
	var out []payTx
	for _, tx := range page {
		out = append(out, convertBTCTx(tx, addr)...)
	}
	sortPayTxsDesc(out)
	return out, nil
}

// fetchBTCHistory paginates Esplora /address/{addr}/txs up to
// payBTCHistoryMaxPages pages (S6), stopping once block_time < since.Unix().
func fetchBTCHistory(ctx context.Context, addr string, since time.Time) ([]payTx, payHistoryScope, error) {
	ctx, cancel := context.WithTimeout(ctx, payUpstreamTimeout)
	defer cancel()

	bases := esploraAPIBases()
	scope := payHistoryScope{Hosts: hostsOf(bases[0])}
	sinceSec := since.Unix()

	var (
		out          []payTx
		lastChainTx  string
		reachedSince bool
	)

	for page := 1; page <= payBTCHistoryMaxPages; page++ {
		path := "/address/" + addr + "/txs"
		if page > 1 {
			path = "/address/" + addr + "/txs/chain/" + lastChainTx
		}
		raw, code := esploraGet(ctx, path)
		if code != "" {
			return nil, scope, errors.New(code)
		}
		var pageTxs []esploraTx
		if err := json.Unmarshal(raw, &pageTxs); err != nil {
			return nil, scope, errors.New("bad_response")
		}
		if len(pageTxs) == 0 {
			reachedSince = true
			break
		}

		lastChainTx = ""
		for _, tx := range pageTxs {
			if tx.Status.Confirmed {
				if tx.Status.BlockTime < sinceSec {
					reachedSince = true
					break
				}
				lastChainTx = tx.TxID
			}
			scope.Transactions++
			out = append(out, convertBTCTx(tx, addr)...)
		}

		if reachedSince || lastChainTx == "" {
			break
		}
		if page == payBTCHistoryMaxPages {
			scope.Truncated = true
		}
	}

	sortPayTxsDesc(out)
	return out, scope, nil
}
