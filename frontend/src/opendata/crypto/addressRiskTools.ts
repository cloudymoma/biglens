// Free third-party investigation tools (spec §11.3, URL templates verified
// 2026-09-26). Frontend-only: nothing is fetched until the user clicks, and
// the link text says the address is sent to that site. Text labels only — no
// logos (Etherscan brand terms), no iframes.

import type { AddressRiskChain, AddressRiskSource } from '../../types';

export type AddressFamily = 'evm' | 'tron' | 'btc';

export interface RiskChainOption {
  id: AddressRiskChain;
  label: string;
  family: AddressFamily;
  placeholder: string;
  formatHint: string;
  localSources: string[];
}

export const RISK_CHAINS: RiskChainOption[] = [
  { id: 'eth', label: 'Ethereum', family: 'evm', placeholder: '0x… address', formatHint: 'Enter a 0x address (42 characters). ENS names are not supported.', localSources: ['ofac', 'mew_darklist', 'stablecoin', 'scam_lookalikes'] },
  { id: 'arb', label: 'Arbitrum', family: 'evm', placeholder: '0x… address', formatHint: 'Enter a 0x address (42 characters). ENS names are not supported.', localSources: ['ofac', 'mew_darklist', 'scam_lookalikes'] },
  { id: 'op', label: 'Optimism', family: 'evm', placeholder: '0x… address', formatHint: 'Enter a 0x address (42 characters). ENS names are not supported.', localSources: ['ofac', 'mew_darklist', 'scam_lookalikes'] },
  { id: 'base', label: 'Base', family: 'evm', placeholder: '0x… address', formatHint: 'Enter a 0x address (42 characters). ENS names are not supported.', localSources: ['ofac', 'mew_darklist', 'scam_lookalikes'] },
  { id: 'tron', label: 'TRON', family: 'tron', placeholder: 'T… address', formatHint: 'Enter a TRON address starting with T (34 base58 characters).', localSources: ['ofac', 'tron_stablecoin', 'scam_lookalikes'] },
  { id: 'btc', label: 'Bitcoin', family: 'btc', placeholder: '1…, 3…, or bc1… address', formatHint: 'Enter a Bitcoin mainnet address (1…, 3…, or bc1…).', localSources: ['ofac'] },
];

const EVM_ADDRESS_RE = /^0x[0-9a-fA-F]{40}$/;
const TRON_ADDRESS_RE = /^T[1-9A-HJ-NP-Za-km-z]{33}$/;
const BTC_ADDRESS_RE = /^(?:[13][1-9A-HJ-NP-Za-km-z]{25,33}|(?:bc1|BC1)[02-9ac-hj-np-zAC-HJ-NP-Z]{6,87})$/;

export function detectAddressFamily(raw: string): AddressFamily | null {
  const s = raw.trim();
  if (!s) return null;
  if (EVM_ADDRESS_RE.test(s)) return 'evm';
  if (TRON_ADDRESS_RE.test(s)) return 'tron';
  if (BTC_ADDRESS_RE.test(s)) return 'btc';
  return null;
}

export function isValidAddressForChain(chain: AddressRiskChain, raw: string): boolean {
  const opt = RISK_CHAINS.find(c => c.id === chain) ?? RISK_CHAINS[0];
  return detectAddressFamily(raw) === opt.family;
}

export interface RiskTool {
  id: string;
  label: string;
  labelByChain?: Partial<Record<AddressRiskChain, string>>;
  hint: string;
  chains: AddressRiskChain[];
  url: (addr: string, chain?: AddressRiskChain) => string;
}

const EXPLORER_URL: Record<AddressRiskChain, (addr: string) => string> = {
  eth: a => `https://etherscan.io/address/${a}`,
  arb: a => `https://arbiscan.io/address/${a}`,
  op: a => `https://optimistic.etherscan.io/address/${a}`,
  base: a => `https://basescan.org/address/${a}`,
  tron: a => `https://tronscan.org/#/address/${a}`,
  btc: a => `https://mempool.space/address/${a}`,
};

const METASLEUTH_SLUG: Record<AddressRiskChain, string> = {
  eth: 'eth',
  arb: 'arbitrum',
  op: 'optimism',
  base: 'base',
  tron: 'tron',
  btc: 'btc',
};

const OKLINK_SLUG: Record<AddressRiskChain, string> = {
  eth: 'ethereum',
  arb: 'arbitrum-one',
  op: 'optimism',
  base: 'base',
  tron: 'tron',
  btc: 'bitcoin',
};

const BLOCKSCOUT_HOST: Record<AddressRiskChain, string> = {
  eth: 'https://eth.blockscout.com',
  arb: 'https://arbitrum.blockscout.com',
  op: 'https://explorer.optimism.io',
  base: 'https://base.blockscout.com',
  tron: 'https://eth.blockscout.com',
  btc: 'https://eth.blockscout.com',
};

export const RISK_TOOLS: RiskTool[] = [
  {
    id: 'etherscan',
    label: 'Etherscan',
    labelByChain: {
      eth: 'Etherscan',
      arb: 'Arbiscan',
      op: 'Optimistic Etherscan',
      base: 'Basescan',
      tron: 'Tronscan',
      btc: 'mempool.space',
    },
    hint: 'public name tags & warnings',
    chains: ['eth', 'arb', 'op', 'base', 'tron', 'btc'],
    url: (a, c = 'eth') => EXPLORER_URL[c](a),
  },
  {
    id: 'misttrack',
    label: 'MistTrack',
    hint: 'third-party risk score — not endorsed by BigLens',
    chains: ['eth', 'arb', 'op', 'base', 'tron', 'btc'],
    url: (a, c = 'eth') => {
      const coin = c === 'tron' ? 'TRX' : c === 'btc' ? 'BTC' : 'ETH';
      return `https://misttrack.io/aml_risks/${coin}/${a}`;
    },
  },
  {
    id: 'chainabuse',
    label: 'Chainabuse',
    hint: 'community reports, not independently checked',
    chains: ['eth', 'arb', 'op', 'base', 'tron', 'btc'],
    url: a => `https://chainabuse.com/address/${a}`,
  },
  {
    id: 'arkham',
    label: 'Arkham',
    hint: 'entity attribution; may require free login',
    chains: ['eth', 'arb', 'op', 'base', 'tron', 'btc'],
    url: a => `https://arkm.com/explorer/address/${a}`,
  },
  {
    id: 'metasleuth',
    label: 'MetaSleuth',
    hint: 'multi-hop fund-flow graph',
    chains: ['eth', 'arb', 'op', 'base', 'tron', 'btc'],
    url: (a, c = 'eth') => `https://metasleuth.io/result/${METASLEUTH_SLUG[c]}/${a}`,
  },
  {
    id: 'oklink',
    label: 'OKLink',
    hint: 'second-opinion labels',
    chains: ['eth', 'arb', 'op', 'base', 'tron', 'btc'],
    url: (a, c = 'eth') => `https://www.oklink.com/${OKLINK_SLUG[c]}/address/${a}`,
  },
  {
    id: 'debank',
    label: 'DeBank',
    hint: 'wallet age & activity (does not rate addresses)',
    chains: ['eth', 'arb', 'op', 'base'],
    url: a => `https://debank.com/profile/${a}`,
  },
  {
    id: 'blockscout',
    label: 'Blockscout',
    hint: 'open-source fallback explorer',
    chains: ['eth', 'arb', 'op', 'base'],
    url: (a, c = 'eth') => `${BLOCKSCOUT_HOST[c]}/address/${a}`,
  },
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
  tron_stablecoin: { label: 'Frozen', color: '#f87171' },
  mew_darklist: { label: 'MEW', color: '#fb923c' },
  scam_lookalikes: { label: 'Lookalike', color: '#fb923c' },
};

// Display names for source ids returned by the backend.
export const RISK_SOURCE_LABELS: Record<string, string> = {
  ofac: 'OFAC SDN (0xB10C)',
  mew_darklist: 'MEW darklist',
  stablecoin: 'USDT/USDC freezes',
  tron_stablecoin: 'USDT freezes (TRON)',
  scam_lookalikes: 'Address-poisoning lookalikes',
  issuer_freeze: 'Issuer freeze (live)',
  chainalysis_oracle: 'Chainalysis oracle',
  goplus: 'GoPlus',
  blockscout: 'Blockscout',
  etherscan: 'Etherscan / Blockscout',
};

const ERROR_TEXT: Record<string, string> = {
  timeout: 'timeout',
  network_error: 'network error',
  rate_limited: 'rate limited',
  pending: 'data pending',
  bad_response: 'unexpected response',
  unavailable: 'local database unavailable',
  key_invalid: 'API key rejected',
  key_throttled: 'key checks throttled',
  rate_limited_local: 'local rate limit reached',
  local_pool_unavailable: 'local lists unavailable',
};

// Query health in neutral words — never a green check (spec §8.2).
export function sourceState(s: AddressRiskSource): string {
  switch (s.status) {
    case 'ok': return 'checked';
    case 'stale': return 'stale data';
    case 'empty': return 'not synced yet';
    case 'partial': return 'partial coverage';
    case 'not_configured': return 'not configured';
    default: {
      const e = s.error ?? '';
      if (e.startsWith('upstream_http_')) return `upstream HTTP ${e.slice('upstream_http_'.length)}`;
      return ERROR_TEXT[e] ?? 'error';
    }
  }
}

export function ago(iso: string): string {
  const mins = Math.max(0, Math.round((Date.now() - Date.parse(iso)) / 60000));
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins} min ago`;
  const h = Math.round(mins / 60);
  return h < 48 ? `${h} h ago` : `${Math.round(h / 24)} days ago`;
}

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
  { id: 'tron_stablecoin', delay: 'complete UTC days; up to ~1 day behind', terms: 'public on-chain data via BigQuery', link: 'https://console.cloud.google.com/marketplace/product/public-data-finance/crypto-tron-blockchain' },
  { id: 'scam_lookalikes', delay: 'complete UTC days; rolling 90-day corpus (same-day pairing lower bound)', terms: 'public on-chain data via BigQuery', link: 'https://console.cloud.google.com/marketplace/product/ethereum/crypto-ethereum-blockchain' },
  { id: 'issuer_freeze', delay: 'live per lookup; USDT/USDT0/USDC contract blacklist functions', terms: 'public contract state', link: 'https://tether.to' },
  { id: 'chainalysis_oracle', delay: 'live per lookup; oracle last updated 2026-03', terms: 'public contract; Chainalysis makes no warranty of accuracy', link: 'https://go.chainalysis.com/chainalysis-oracle-docs.html' },
  { id: 'goplus', delay: 'live per lookup', terms: 'free public API', link: 'https://gopluslabs.io' },
  { id: 'blockscout', delay: 'live per lookup (scam reputation & public security tags)', terms: 'Blockscout public API', link: 'https://eth.blockscout.com' },
  { id: 'etherscan', delay: 'live per lookup (Blockscout keyless fallback when no Etherscan key); newest 1000 rows per list + live counterparty screening', terms: 'Etherscan API terms (personal, non-commercial use) or Blockscout public API', link: 'https://etherscan.io/apiterms' },
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
