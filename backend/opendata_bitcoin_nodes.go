package main

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

var btcBitnodesLatestURL = "https://bitnodes.io/api/v1/snapshots/latest/"

const (
	btcBitnodesMaxBody int64 = 16 << 20 // 16 MiB cap for the ~2.7 MB Bitnodes snapshot
	btcDBIPAttribution       = "IP geolocation by DB-IP (https://db-ip.com, CC BY 4.0)"
)

// btcCloudASNs maps the named cloud/hosting ASNs in §10.2 to their display provider label.
var btcCloudASNs = map[uint32]string{
	16509:  "AWS",
	14618:  "AWS",
	15169:  "Google Cloud",
	396982: "Google Cloud",
	8075:   "Microsoft Azure",
	24940:  "Hetzner",
	16276:  "OVH",
	14061:  "DigitalOcean",
	63949:  "Akamai / Linode",
	20473:  "Vultr",
	31898:  "Oracle Cloud",
	51167:  "Contabo",
	45102:  "Alibaba Cloud",
}

type btcBitnodesSnapshot struct {
	Timestamp    int64                      `json:"timestamp"`
	TotalNodes   int64                      `json:"total_nodes"`
	LatestHeight int64                      `json:"latest_height"`
	Nodes        map[string]json.RawMessage `json:"nodes"`
}

type btcNodeAgg struct {
	AsOf             string
	Reachable        int64
	Clearnet         int64
	Network          BtcNodeNetworkMix
	ClientCounts     map[string]int64
	CoreVersionCount map[string]int64
	ClearnetIPCounts map[netip.Addr]int64
	SortedIPs        []netip.Addr
}

func fetchBitnodesSnapshot(ctx context.Context) (*btcBitnodesSnapshot, string) {
	var snap btcBitnodesSnapshot
	if code := btcGetJSON(ctx, btcBitnodesLatestURL, btcBitnodesMaxBody, &snap); code != "" {
		return nil, code
	}
	if len(snap.Nodes) == 0 {
		return nil, "bad_response"
	}
	return &snap, ""
}

func splitBitnodesHost(addrPort string) string {
	addrPort = strings.TrimSpace(addrPort)
	if h, _, err := net.SplitHostPort(addrPort); err == nil {
		return strings.Trim(h, "[]")
	}
	if idx := strings.LastIndex(addrPort, ":"); idx > 0 {
		return strings.Trim(addrPort[:idx], "[]")
	}
	return strings.Trim(addrPort, "[]")
}

func parseBitnodesUserAgent(raw json.RawMessage) string {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) < 2 {
		return ""
	}
	var ua string
	_ = json.Unmarshal(arr[1], &ua)
	return strings.TrimSpace(ua)
}

func classifyBitnodesClient(ua string) (client string, coreMajor string) {
	lower := strings.ToLower(ua)
	switch {
	case strings.Contains(lower, "knots"):
		return "Bitcoin Knots", ""
	case strings.Contains(ua, "Satoshi:") || strings.Contains(lower, "satoshi:"):
		idx := strings.Index(lower, "satoshi:")
		ver := ua[idx+len("satoshi:"):]
		ver = strings.TrimPrefix(ver, "/")
		if slash := strings.IndexAny(ver, "/("); slash >= 0 {
			ver = ver[:slash]
		}
		parts := strings.Split(strings.TrimSpace(ver), ".")
		if len(parts) >= 2 && parts[0] == "0" {
			return "Bitcoin Core", "0." + parts[1] + ".x"
		}
		if len(parts) >= 1 && parts[0] != "" {
			return "Bitcoin Core", parts[0] + ".x"
		}
		return "Bitcoin Core", "Other"
	case strings.Contains(lower, "btcd"):
		return "btcd", ""
	case strings.Contains(lower, "bcoin"):
		return "bcoin", ""
	default:
		return "Other", ""
	}
}

func aggregateBitnodes(snap *btcBitnodesSnapshot) btcNodeAgg {
	agg := btcNodeAgg{
		ClientCounts:     make(map[string]int64),
		CoreVersionCount: make(map[string]int64),
		ClearnetIPCounts: make(map[netip.Addr]int64),
	}
	if snap == nil {
		return agg
	}
	if snap.Timestamp > 0 {
		agg.AsOf = time.Unix(snap.Timestamp, 0).UTC().Format(time.RFC3339)
	}
	for addrPort, rawVal := range snap.Nodes {
		agg.Reachable++
		host := strings.ToLower(splitBitnodesHost(addrPort))
		switch {
		case strings.HasSuffix(host, ".onion"):
			agg.Network.Tor++
		case strings.HasSuffix(host, ".b32.i2p") || strings.HasSuffix(host, ".i2p"):
			agg.Network.I2P++
		default:
			if ip, err := netip.ParseAddr(host); err == nil {
				ip = ip.Unmap()
				if ip.Is6() {
					b16 := ip.As16()
					if b16[0] == 0xfc {
						agg.Network.CJDNS++
					} else {
						agg.Network.IPv6++
						agg.Clearnet++
						agg.ClearnetIPCounts[ip]++
					}
				} else if ip.Is4() {
					agg.Network.IPv4++
					agg.Clearnet++
					agg.ClearnetIPCounts[ip]++
				}
			}
		}

		ua := parseBitnodesUserAgent(rawVal)
		client, coreVer := classifyBitnodesClient(ua)
		agg.ClientCounts[client]++
		if coreVer != "" {
			agg.CoreVersionCount[coreVer]++
		}
	}

	agg.SortedIPs = make([]netip.Addr, 0, len(agg.ClearnetIPCounts))
	for ip := range agg.ClearnetIPCounts {
		agg.SortedIPs = append(agg.SortedIPs, ip)
	}
	slices.SortFunc(agg.SortedIPs, func(a, b netip.Addr) int {
		return a.Compare(b)
	})
	return agg
}

func sortedNodeBuckets(counts map[string]int64, denom int64, maxItems int) []BtcNodeBucket {
	if len(counts) == 0 || denom <= 0 {
		return nil
	}
	out := make([]BtcNodeBucket, 0, len(counts))
	for label, c := range counts {
		if c <= 0 {
			continue
		}
		out = append(out, BtcNodeBucket{
			Label:    label,
			Count:    c,
			SharePct: float64(c) / float64(denom) * 100.0,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Label < out[j].Label
	})
	if maxItems > 0 && len(out) > maxItems {
		var otherCount int64
		for _, b := range out[maxItems:] {
			otherCount += b.Count
		}
		out = out[:maxItems]
		if otherCount > 0 {
			out = append(out, BtcNodeBucket{
				Label:    "Other",
				Count:    otherCount,
				SharePct: float64(otherCount) / float64(denom) * 100.0,
			})
		}
	}
	return out
}

func computeNodeDistribution(agg btcNodeAgg, geoByIP map[netip.Addr]geoHit) BtcNodes {
	var totalCore int64
	for _, c := range agg.CoreVersionCount {
		totalCore += c
	}
	clients := sortedNodeBuckets(agg.ClientCounts, agg.Reachable, 0)
	coreVersions := sortedNodeBuckets(agg.CoreVersionCount, max(totalCore, 1), 10)

	countryCounts := make(map[string]int64)
	asnCounts := make(map[uint32]int64)
	asnOrgs := make(map[uint32]string)
	var countryResolved int64
	var asnResolved int64
	var cloudCount int64
	var unresolved int64

	for ip, nodeCount := range agg.ClearnetIPCounts {
		hit, ok := geoByIP[ip]
		if !ok || (hit.Country == "" && hit.ASN == 0) {
			unresolved += nodeCount
			continue
		}
		cc := strings.ToUpper(strings.TrimSpace(hit.Country))
		if cc != "" && cc != "ZZ" {
			countryCounts[cc] += nodeCount
			countryResolved += nodeCount
		} else {
			unresolved += nodeCount
		}
		if hit.ASN > 0 {
			asnCounts[hit.ASN] += nodeCount
			asnResolved += nodeCount
			if asnOrgs[hit.ASN] == "" && hit.Org != "" {
				asnOrgs[hit.ASN] = hit.Org
			}
			if _, isCloud := btcCloudASNs[hit.ASN]; isCloud {
				cloudCount += nodeCount
			}
		}
	}

	denom := agg.Clearnet
	var clearnetCovPct float64
	if agg.Reachable > 0 {
		clearnetCovPct = float64(denom) / float64(agg.Reachable) * 100.0
	}

	countries := make([]BtcNodeCountry, 0, len(countryCounts))
	for cc, c := range countryCounts {
		var share float64
		if denom > 0 {
			share = float64(c) / float64(denom) * 100.0
		}
		countries = append(countries, BtcNodeCountry{
			Code:     cc,
			Name:     btcCountryName(cc),
			Count:    c,
			SharePct: share,
		})
	}
	sort.Slice(countries, func(i, j int) bool {
		if countries[i].Count != countries[j].Count {
			return countries[i].Count > countries[j].Count
		}
		return countries[i].Code < countries[j].Code
	})

	asnRows := make([]BtcNodeASN, 0, len(asnCounts))
	asnShareMap := make(map[string]int64, len(asnCounts)+1)
	for asn, c := range asnCounts {
		var share float64
		if denom > 0 {
			share = float64(c) / float64(denom) * 100.0
		}
		org := asnOrgs[asn]
		if org == "" {
			if label, ok := btcCloudASNs[asn]; ok {
				org = label
			}
		}
		_, isCloud := btcCloudASNs[asn]
		asnRows = append(asnRows, BtcNodeASN{
			ASN:      asn,
			Org:      org,
			Count:    c,
			SharePct: share,
			IsCloud:  isCloud,
		})
		asnShareMap[strconv.FormatUint(uint64(asn), 10)] = c
	}
	if denom > asnResolved {
		asnShareMap["Unknown"] = denom - asnResolved
	}
	sort.Slice(asnRows, func(i, j int) bool {
		if asnRows[i].Count != asnRows[j].Count {
			return asnRows[i].Count > asnRows[j].Count
		}
		return asnRows[i].ASN < asnRows[j].ASN
	})
	totalASNs := int64(len(asnRows))
	if len(asnRows) > 20 {
		asnRows = asnRows[:20]
	}

	countryShareMap := make(map[string]int64, len(countryCounts)+1)
	for cc, c := range countryCounts {
		countryShareMap[cc] = c
	}
	if denom > countryResolved {
		countryShareMap["Unknown"] = denom - countryResolved
	}

	var cloudSharePct float64
	if denom > 0 {
		cloudSharePct = float64(cloudCount) / float64(denom) * 100.0
	}

	return BtcNodes{
		AsOf:         agg.AsOf,
		Reachable:    agg.Reachable,
		Clearnet:     agg.Clearnet,
		Network:      agg.Network,
		Clients:      clients,
		CoreVersions: coreVersions,
		Countries:    countries,
		TotalASNs:    totalASNs,
		ASNs:         asnRows,
		CloudShare: BtcConcentrationMetric{
			Value:       cloudSharePct,
			Denominator: denom,
			Unit:        "clearnet nodes",
			CoveragePct: clearnetCovPct,
			Method:      "DB-IP ASN (named cloud/hosting ASN list)",
		},
		ASNNakamoto: BtcConcentrationMetric{
			Value:       float64(nakamoto(asnShareMap, "Unknown")),
			Denominator: denom,
			Unit:        "clearnet nodes",
			CoveragePct: clearnetCovPct,
			Method:      "DB-IP ASN",
		},
		CountryNakamoto: BtcConcentrationMetric{
			Value:       float64(nakamoto(countryShareMap, "Unknown")),
			Denominator: denom,
			Unit:        "clearnet nodes",
			CoveragePct: clearnetCovPct,
			Method:      "DB-IP country",
		},
		ASNHHI: BtcConcentrationMetric{
			Value:       hhi(asnShareMap),
			Denominator: denom,
			Unit:        "clearnet nodes",
			CoveragePct: clearnetCovPct,
			Method:      "DB-IP ASN Σ(share%²)",
		},
		Unresolved:  unresolved,
		Attribution: btcDBIPAttribution,
	}
}
