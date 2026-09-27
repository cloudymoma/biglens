// Free third-party investigation tools (spec §11.3, URL templates verified
// 2026-09-26). Frontend-only: nothing is fetched until the user clicks, and
// the link text says the address is sent to that site. Text labels only — no
// logos (Etherscan brand terms), no iframes.

export interface RiskTool {
  id: string;
  label: string;
  hint: string;
  url: (addr: string) => string;
}

export const RISK_TOOLS: RiskTool[] = [
  { id: 'etherscan', label: 'Etherscan', hint: 'public name tags & warnings', url: a => `https://etherscan.io/address/${a}` },
  { id: 'misttrack', label: 'MistTrack', hint: 'third-party risk score — not endorsed by BigLens', url: a => `https://misttrack.io/aml_risks/ETH/${a}` },
  { id: 'chainabuse', label: 'Chainabuse', hint: 'community reports, not independently checked', url: a => `https://chainabuse.com/address/${a}` },
  { id: 'arkham', label: 'Arkham', hint: 'entity attribution; may require free login', url: a => `https://arkm.com/explorer/address/${a}` },
  { id: 'metasleuth', label: 'MetaSleuth', hint: 'multi-hop fund-flow graph', url: a => `https://metasleuth.io/result/eth/${a}` },
  { id: 'oklink', label: 'OKLink', hint: 'second-opinion labels', url: a => `https://www.oklink.com/ethereum/address/${a}` },
  { id: 'debank', label: 'DeBank', hint: 'wallet age & activity (does not rate addresses)', url: a => `https://debank.com/profile/${a}` },
  { id: 'blockscout', label: 'Blockscout', hint: 'open-source fallback explorer', url: a => `https://eth.blockscout.com/address/${a}` },
];

// Revoke.cash checks the user's own wallet approvals (it asks them to connect
// a wallet), so it is linked without the looked-up address and shown apart
// from the counterparty-risk tools.
export const REVOKE_CASH_URL = 'https://revoke.cash/';

// Short badge text for the local lists an address is on (Whales & Flow).
// Text, not color alone, carries the meaning.
export const RISK_BADGES: Record<string, { label: string; color: string }> = {
  ofac: { label: 'OFAC', color: '#f87171' },
  stablecoin: { label: 'Frozen', color: '#f87171' },
  mew_darklist: { label: 'MEW', color: '#fb923c' },
};

// Display names for source ids returned by the backend.
export const RISK_SOURCE_LABELS: Record<string, string> = {
  ofac: 'OFAC SDN (0xB10C)',
  mew_darklist: 'MEW darklist',
  stablecoin: 'USDT/USDC freezes',
  chainalysis_oracle: 'Chainalysis oracle',
  goplus: 'GoPlus',
  etherscan: 'Etherscan',
};

export interface RiskSourceMeta {
  id: string;
  delay: string;
  terms: string;
  link: string;
}

// Static rows of the overview Sources table (spec §9): it doubles as the
// attribution and licensing notice, so every source is named in text.
export const RISK_SOURCE_META: RiskSourceMeta[] = [
  { id: 'ofac', delay: 'synced every 6 h; the 0xB10C extract updates nightly', terms: 'MIT (0xB10C); OFAC data is public', link: 'https://github.com/0xB10C/ofac-sanctioned-digital-currency-addresses' },
  { id: 'mew_darklist', delay: 'historical list, unchanged since 2020-11', terms: 'MIT', link: 'https://github.com/MyEtherWallet/ethereum-lists' },
  { id: 'stablecoin', delay: 'complete UTC days; up to ~1 day behind', terms: 'public on-chain data via BigQuery', link: 'https://console.cloud.google.com/marketplace/product/ethereum/crypto-ethereum-blockchain' },
  { id: 'chainalysis_oracle', delay: 'live per lookup; oracle last updated 2026-03', terms: 'public contract; Chainalysis makes no warranty of accuracy', link: 'https://go.chainalysis.com/chainalysis-oracle-docs.html' },
  { id: 'goplus', delay: 'live per lookup', terms: 'free public API', link: 'https://gopluslabs.io' },
  { id: 'etherscan', delay: 'live per lookup with a key; newest 1000 rows per list', terms: 'Etherscan API terms: personal, non-commercial use; data provided by Etherscan', link: 'https://etherscan.io/apiterms' },
];

export const ETHERSCAN_SIGNUP_URL = 'https://etherscan.io/myapikey';
export const ETHERSCAN_HELP_URL = 'https://docs.etherscan.io/set-up-your-api-key';

// Shown next to the key panel at all times (spec §10).
export const ETHERSCAN_TERMS_NOTE =
  "Etherscan's free API terms allow personal, non-commercial use only. The key and its results are shared by " +
  "everyone using this BigLens instance — don't configure a key on an instance other people use. Stored in the " +
  "server's conf.yaml.";

// Shown when freeze history is missing or partial. Without --yes the CLI only
// prints a dry-run cost estimate.
export const BACKFILL_COMMAND = 'cd /opt/biglens/backend && sudo -u biglens ./biglens-server --address-risk-backfill';
