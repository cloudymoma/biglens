package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"time"

	"cloud.google.com/go/civil"
)

// Freeze activity for the Controls & Freezes tab (stablecoin.md §7.2 tab 6,
// §8.3): read-only queries on Address Risk's freeze history
// (stablecoin_events for Ethereum, tron_stablecoin_events for TRON), plus
// Tether's quarantined amounts from the latest stored Tether update. No
// upstream calls.

const stableFreezeRecentMax = 10

var stableFreezeWindowDays = []int{30, 90}

// stableFreezeSources lists the synced tokens. Table and Source are Go
// constants, never request input.
var stableFreezeSources = []struct {
	Asset, Chain, Token, Table, Source string
	CanDestroy                         bool // TetherToken destroyBlackFunds; FiatToken has none
}{
	{"usdt", "eth", "USDT", "stablecoin_events", stablecoinSourceID, true},
	{"usdt", "tron", "USDT", "tron_stablecoin_events", tronStablecoinSourceID, true},
	{"usdc", "eth", "USDC", "stablecoin_events", stablecoinSourceID, false},
	{"eurc", "eth", "EURC", "stablecoin_events", stablecoinSourceID, false},
}

// StableFreezeWindow counts one token's events over the days From..To
// (inclusive; To is the last synced day).
type StableFreezeWindow struct {
	Days      int    `json:"days"`
	From      string `json:"from"`
	To        string `json:"to"`
	Freezes   int    `json:"freezes"`
	Unfreezes int    `json:"unfreezes"`
	Destroys  int    `json:"destroys"`
	// DestroyedNative is whole tokens with 2 decimals; null where the
	// contract cannot destroy frozen funds (USDC, EURC).
	DestroyedNative *string `json:"destroyed_native"`
	Partial         bool    `json:"partial"` // the token's history starts inside the window
}

type StableFreezeEvent struct {
	Action    string `json:"action"` // freeze | unfreeze | destroy
	Address   string `json:"address"`
	Amount    string `json:"amount,omitempty"` // destroy: whole tokens, 2 decimals
	TxHash    string `json:"tx_hash"`
	TxURL     string `json:"tx_url"`
	BlockTime string `json:"block_time"`
}

type StableFreezeStats struct {
	Asset  string `json:"asset"`
	Chain  string `json:"chain"`
	Token  string `json:"token"`
	Source string `json:"source"` // the Address Risk sync: stablecoin_logs | tron_stablecoin_logs
	Status string `json:"status"` // ok | not_synced
	// CoverageFrom..Cursor is the synced history (EURC: from the day the
	// sync started including it; no backfill).
	CoverageFrom string `json:"coverage_from,omitempty"`
	Cursor       string `json:"cursor,omitempty"`
	CoverageNote string `json:"coverage_note,omitempty"`
	// FrozenNow counts addresses whose latest event is a freeze or destroy.
	FrozenNow *int                 `json:"frozen_now"`
	Windows   []StableFreezeWindow `json:"windows"`
	Recent    []StableFreezeEvent  `json:"recent"`
}

// stableFreezeStats builds the freeze activity of one asset ("all": every
// synced token).
func (s *riskStore) stableFreezeStats(ctx context.Context, asset string) ([]StableFreezeStats, error) {
	states, err := s.allSyncStates(ctx)
	if err != nil {
		return nil, err
	}
	tokenCov, err := s.stablecoinTokenCoverage(ctx)
	if err != nil {
		return nil, err
	}
	var ethStates, tronStates []stablecoinState
	out := []StableFreezeStats{}
	for _, src := range stableFreezeSources {
		if asset != "all" && asset != src.Asset {
			continue
		}
		st := states[src.Source]
		fs := StableFreezeStats{Asset: src.Asset, Chain: src.Chain, Token: src.Token, Source: src.Source, Status: "not_synced",
			CoverageFrom: st.CoverageFrom, Cursor: st.Cursor, Windows: []StableFreezeWindow{}, Recent: []StableFreezeEvent{}}
		late := false
		for _, tk := range stablecoinLateTokens {
			late = late || (src.Chain == "eth" && tk == src.Token)
		}
		if late {
			fs.CoverageFrom = tokenCov[src.Token]
			if fs.CoverageFrom == "" {
				fs.CoverageNote = fmt.Sprintf(riskTokenNotSyncedFmt, src.Token)
			} else {
				fs.CoverageNote = fmt.Sprintf(riskTokenCoverageFmt, src.Token, fs.CoverageFrom)
			}
		}
		cursor, errC := civil.ParseDate(st.Cursor)
		if st.Cursor == "" || fs.CoverageFrom == "" || errC != nil {
			out = append(out, fs)
			continue
		}
		fs.Status = "ok"
		for _, days := range stableFreezeWindowDays {
			w, err := s.stableFreezeWindow(ctx, src.Table, src.Token, src.CanDestroy, cursor.AddDays(-(days - 1)), cursor)
			if err != nil {
				return nil, err
			}
			w.Days, w.Partial = days, fs.CoverageFrom > w.From
			fs.Windows = append(fs.Windows, w)
		}
		// The latest-state rule shared with the Address Risk overview.
		var latest []stablecoinState
		if src.Chain == "tron" {
			if tronStates == nil {
				if tronStates, err = s.tronStablecoinStates(ctx, ""); err != nil {
					return nil, err
				}
			}
			latest = tronStates
		} else {
			if ethStates == nil {
				if ethStates, err = s.stablecoinStates(ctx, ""); err != nil {
					return nil, err
				}
			}
			latest = ethStates
		}
		n := 0
		for _, l := range latest {
			if l.Token == src.Token && l.Action != "unfreeze" {
				n++
			}
		}
		fs.FrozenNow = &n
		if fs.Recent, err = s.stableFreezeRecent(ctx, src.Table, src.Token, src.Chain); err != nil {
			return nil, err
		}
		out = append(out, fs)
	}
	return out, nil
}

func (s *riskStore) stableFreezeWindow(ctx context.Context, table, token string, canDestroy bool, from, to civil.Date) (StableFreezeWindow, error) {
	w := StableFreezeWindow{From: from.String(), To: to.String()}
	rs, err := s.db.QueryContext(ctx, `SELECT action, amount FROM `+table+`
WHERE token = ? AND substr(block_time, 1, 10) >= ? AND substr(block_time, 1, 10) <= ?`, token, w.From, w.To)
	if err != nil {
		return w, fmt.Errorf("freeze window: %w", err)
	}
	defer rs.Close()
	destroyed := new(big.Int)
	for rs.Next() {
		var action, amount string
		if err := rs.Scan(&action, &amount); err != nil {
			return w, err
		}
		switch action {
		case "freeze":
			w.Freezes++
		case "unfreeze":
			w.Unfreezes++
		case "destroy":
			w.Destroys++
			n, ok := parseBaseUnits(amount)
			if !ok {
				return w, fmt.Errorf("bad destroy amount %q", amount)
			}
			destroyed.Add(destroyed, n)
		}
	}
	if err := rs.Err(); err != nil {
		return w, err
	}
	if canDestroy {
		v := formatTokenAmount(destroyed, 6)
		w.DestroyedNative = &v
	}
	return w, nil
}

func (s *riskStore) stableFreezeRecent(ctx context.Context, table, token, chain string) ([]StableFreezeEvent, error) {
	rs, err := s.db.QueryContext(ctx, `SELECT tx_hash, action, address, amount, block_time FROM `+table+`
WHERE token = ? ORDER BY block_number DESC, log_index DESC LIMIT ?`, token, stableFreezeRecentMax)
	if err != nil {
		return nil, fmt.Errorf("recent freezes: %w", err)
	}
	defer rs.Close()
	out := []StableFreezeEvent{}
	for rs.Next() {
		var e StableFreezeEvent
		var amount string
		if err := rs.Scan(&e.TxHash, &e.Action, &e.Address, &amount, &e.BlockTime); err != nil {
			return nil, err
		}
		if n, ok := parseBaseUnits(amount); ok {
			e.Amount = formatTokenAmount(n, 6)
		}
		if info, ok := chains[chain]; ok && info.TxURL != "" {
			e.TxURL = fmt.Sprintf(info.TxURL, e.TxHash)
		}
		out = append(out, e)
	}
	return out, rs.Err()
}

// StableQuarantined is Tether's quarantined USD₮ per chain in the latest
// stored Tether update (native units).
type StableQuarantined struct {
	AsOf        string                   `json:"as_of"`
	UpdateID    int64                    `json:"update_id"`
	TotalNative float64                  `json:"total_native"`
	Chains      []StableQuarantinedChain `json:"chains"` // largest first
}

type StableQuarantinedChain struct {
	Chain             string  `json:"chain"`
	ChainLabel        string  `json:"chain_label"`
	QuarantinedNative float64 `json:"quarantined_native"`
}

// stableTetherQuarantined returns nil when no Tether update is stored yet.
func (s *riskStore) stableTetherQuarantined(ctx context.Context) (*StableQuarantined, error) {
	snaps, err := s.stableTetherSnapshots(ctx, 1)
	if err != nil || len(snaps) == 0 {
		return nil, err
	}
	var rows []struct {
		Name              string  `json:"name"`
		QuarantinedNative float64 `json:"quarantined_native"`
	}
	if err := json.Unmarshal([]byte(snaps[0].ChainsJSON), &rows); err != nil {
		return nil, fmt.Errorf("tether chains_json: %w", err)
	}
	q := &StableQuarantined{AsOf: stableFmt(time.Unix(snaps[0].UpdateID, 0)), UpdateID: snaps[0].UpdateID, Chains: []StableQuarantinedChain{}}
	for _, r := range rows {
		key := stableChainKey("tether", r.Name)
		label := r.Name
		if info, ok := chains[key]; ok {
			label = info.Label
		} else if l, ok := stableChainLabels[key]; ok {
			label = l
		}
		q.Chains = append(q.Chains, StableQuarantinedChain{Chain: key, ChainLabel: label, QuarantinedNative: r.QuarantinedNative})
		q.TotalNative += r.QuarantinedNative
	}
	sort.SliceStable(q.Chains, func(i, j int) bool { return q.Chains[i].QuarantinedNative > q.Chains[j].QuarantinedNative })
	return q, nil
}
