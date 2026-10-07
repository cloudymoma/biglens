import type {
  PaymentAsset,
  PaymentFinalityLevel,
  PaymentNetwork,
  PaymentTokenTier,
} from '../../types';
import {
  detectAddressFamily,
  isValidAddressForChain,
  type AddressFamily,
} from './addressRiskTools';

export { detectAddressFamily, isValidAddressForChain, type AddressFamily };

export interface PayAssetOption {
  id: PaymentAsset | 'BTC' | 'SOL';
  label: string;
  enabled: boolean;
  note?: string;
}

export const PAY_ASSETS: PayAssetOption[] = [
  { id: 'USDT', label: 'USDT', enabled: true },
  { id: 'USDC', label: 'USDC', enabled: true },
  { id: 'ETH', label: 'ETH', enabled: true },
  { id: 'TRX', label: 'TRX', enabled: true },
  { id: 'BTC', label: 'BTC', enabled: false, note: 'coming later' },
  { id: 'SOL', label: 'SOL', enabled: false, note: 'coming later' },
];

// Asset -> supported networks table.
// Must stay in sync with payAssets in backend/chain_registry.go.
export const PAY_ASSET_NETWORKS: Record<PaymentAsset, PaymentNetwork[]> = {
  USDT: ['tron', 'eth', 'arb', 'op', 'base'],
  USDC: ['eth', 'arb', 'op', 'base'],
  ETH: ['eth', 'arb', 'op', 'base'],
  TRX: ['tron'],
};

export interface PayNetworkOption {
  id: PaymentNetwork;
  label: string;
  family: AddressFamily;
  placeholder: string;
  formatHint: string;
  addrExplorer: (addr: string) => string;
  blockExplorer: (block: number) => string;
}

export const PAY_NETWORKS: Record<PaymentNetwork, PayNetworkOption> = {
  tron: {
    id: 'tron',
    label: 'TRON',
    family: 'tron',
    placeholder: 'T… receiving address (34 base58 characters)',
    formatHint: 'Enter a TRON address starting with T (34 base58 characters).',
    addrExplorer: a => `https://tronscan.org/#/address/${a}`,
    blockExplorer: b => `https://tronscan.org/#/block/${b}`,
  },
  eth: {
    id: 'eth',
    label: 'Ethereum',
    family: 'evm',
    placeholder: '0x… receiving address (42 characters)',
    formatHint: 'Enter a 0x address (42 characters). ENS names are not supported.',
    addrExplorer: a => `https://etherscan.io/address/${a}`,
    blockExplorer: b => `https://etherscan.io/block/${b}`,
  },
  arb: {
    id: 'arb',
    label: 'Arbitrum',
    family: 'evm',
    placeholder: '0x… receiving address (42 characters)',
    formatHint: 'Enter a 0x address (42 characters). ENS names are not supported.',
    addrExplorer: a => `https://arbiscan.io/address/${a}`,
    blockExplorer: b => `https://arbiscan.io/block/${b}`,
  },
  op: {
    id: 'op',
    label: 'Optimism',
    family: 'evm',
    placeholder: '0x… receiving address (42 characters)',
    formatHint: 'Enter a 0x address (42 characters). ENS names are not supported.',
    addrExplorer: a => `https://optimistic.etherscan.io/address/${a}`,
    blockExplorer: b => `https://optimistic.etherscan.io/block/${b}`,
  },
  base: {
    id: 'base',
    label: 'Base',
    family: 'evm',
    placeholder: '0x… receiving address (42 characters)',
    formatHint: 'Enter a 0x address (42 characters). ENS names are not supported.',
    addrExplorer: a => `https://basescan.org/address/${a}`,
    blockExplorer: b => `https://basescan.org/block/${b}`,
  },
};

// Official contracts per (asset, network), mirroring tokenRegistry in backend/chain_registry.go.
export const OFFICIAL_CONTRACTS: Record<string, { label: string; contract: string; tier: 'native' | 'bridged' }[]> = {
  'USDT:tron': [{ label: 'USDT', contract: 'TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t', tier: 'native' }],
  'USDT:eth': [{ label: 'USDT', contract: '0xdac17f958d2ee523a2206206994597c13d831ec7', tier: 'native' }],
  'USDT:arb': [{ label: 'USD₮0', contract: '0xfd086bc7cd5c481dcc9c85ebe478a1c0b69fcbb9', tier: 'native' }],
  'USDT:op': [
    { label: 'USD₮0', contract: '0x01bff41798a0bcf287b996046ca68b395dbc1071', tier: 'native' },
    { label: 'USDT (bridged)', contract: '0x94b008aa00579c1307b0ef2c499ad98a8ce58e58', tier: 'bridged' },
  ],
  'USDT:base': [{ label: 'USDT (bridged)', contract: '0xfde4c96c8593536e31f229ea8f37b2ada2699bb2', tier: 'bridged' }],
  'USDC:eth': [{ label: 'USDC', contract: '0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48', tier: 'native' }],
  'USDC:arb': [
    { label: 'USDC', contract: '0xaf88d065e77c8cc2239327c5edb3a432268e5831', tier: 'native' },
    { label: 'USDC.e (bridged)', contract: '0xff970a61a04b1ca14834a43f5de4533ebddb5cc8', tier: 'bridged' },
  ],
  'USDC:op': [
    { label: 'USDC', contract: '0x0b2c639c533813f4aa9d7837caf62653d097ff85', tier: 'native' },
    { label: 'USDC.e (bridged)', contract: '0x7f5c764cbc14f9669b88837ca1490cca17c31607', tier: 'bridged' },
  ],
  'USDC:base': [{ label: 'USDC', contract: '0x833589fcd6edb6e08f4c7c32d4f71b54bda02913', tier: 'native' }],
};

export function networksForAssetAndFamily(asset: PaymentAsset, family: AddressFamily): PaymentNetwork[] {
  return PAY_ASSET_NETWORKS[asset].filter(n => PAY_NETWORKS[n].family === family);
}

export function fmtDurationSec(sec: number): string {
  const s = Math.max(0, Math.round(sec));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  const rem = s % 60;
  return rem > 0 ? `${m}m ${rem}s` : `${m}m`;
}

// Settlement level copy (spec §5.2).
export function levelCopy(level: PaymentFinalityLevel, estSecLeft: number): string {
  switch (level) {
    case 'DANGER':
      return 'Failed / not genuine. Do not treat this as received.';
    case 'SOFT': {
      const eta = estSecLeft > 0 ? ` ~${fmtDurationSec(estSecLeft)} to final.` : '';
      return `In a block, but can still be reversed by a reorg.${eta}`;
    }
    case 'SAFE': {
      const eta = estSecLeft > 0 ? ` (~${fmtDurationSec(estSecLeft)} to final)` : '';
      return `Past the chain's safe head${eta}. A reversal would need an extraordinary reorg.`;
    }
    case 'FINALIZED':
      return 'Final by consensus — cannot be reorged. Stablecoin issuers can still freeze addresses.';
  }
}

export function levelColors(level: PaymentFinalityLevel): {
  text: string;
  bg: string;
  border: string;
  bar: string;
} {
  switch (level) {
    case 'DANGER':
      return {
        text: 'text-red-300',
        bg: 'bg-red-500/15',
        border: 'border-red-500/40',
        bar: '#ef4444',
      };
    case 'SOFT':
      return {
        text: 'text-amber-300',
        bg: 'bg-amber-500/15',
        border: 'border-amber-500/40',
        bar: '#f59e0b',
      };
    case 'SAFE':
      return {
        text: 'text-sky-300',
        bg: 'bg-sky-500/15',
        border: 'border-sky-500/40',
        bar: '#38bdf8',
      };
    case 'FINALIZED':
      return {
        text: 'text-emerald-300',
        bg: 'bg-emerald-500/15',
        border: 'border-emerald-500/40',
        bar: '#10b981',
      };
  }
}

export function tierBadgeInfo(tier: PaymentTokenTier): {
  label: string;
  detail: string;
  className: string;
} {
  switch (tier) {
    case 'native':
      return {
        label: '✅ official',
        detail: 'Official issuer-native asset on this network.',
        className: 'bg-emerald-500/15 text-emerald-300 border-emerald-500/30',
      };
    case 'bridged':
      return {
        label: '⚠ bridged',
        detail: 'bridged version — not issuer-native; exchanges may not credit it',
        className: 'bg-amber-500/15 text-amber-300 border-amber-500/30',
      };
    case 'counterfeit':
      return {
        label: '🚨 counterfeit',
        detail: 'Unregistered token contract mimicking the official asset symbol — not genuine funds.',
        className: 'bg-red-500/15 text-red-300 border-red-500/30',
      };
    case 'other':
      return {
        label: 'unrelated token',
        detail: 'Unregistered token contract with a different symbol.',
        className: 'bg-zinc-800 text-zinc-400 border-zinc-700',
      };
  }
}

export interface FlagMeta {
  id: string;
  label: string;
  severity: 'critical' | 'warning' | 'info';
  description: string;
}

export const FLAG_META: Record<string, FlagMeta> = {
  counterfeit_token: {
    id: 'counterfeit_token',
    label: 'counterfeit token',
    severity: 'critical',
    description:
      'Token contract is not in the official registry for this asset and network, but its symbol mimics the asset.',
  },
  sent_to_lookalike: {
    id: 'sent_to_lookalike',
    label: 'sent to lookalike',
    severity: 'critical',
    description:
      'Real outgoing transfer to an address sharing the first 4 and last 4 characters with an earlier counterparty (possible address-poisoning victim transfer).',
  },
  counterparty_listed: {
    id: 'counterparty_listed',
    label: 'counterparty listed',
    severity: 'critical',
    description:
      'Counterparty address matches local OFAC SDN, MEW darklist, or Ethereum USDT/USDC freeze lists.',
  },
  failed: {
    id: 'failed',
    label: 'failed tx',
    severity: 'critical',
    description: 'Transaction reverted or failed on-chain; no funds were transferred.',
  },
  lookalike: {
    id: 'lookalike',
    label: 'address poisoning / lookalike',
    severity: 'warning',
    description:
      'Counterparty shares the first 4 and last 4 characters with an earlier trusted counterparty in the 7-day window.',
  },
  zero_value: {
    id: 'zero_value',
    label: 'zero-value transfer',
    severity: 'warning',
    description:
      'Zero-value transfer on an official token contract — commonly used to plant lookalike addresses in transaction history without the owner’s signature.',
  },
  dust: {
    id: 'dust',
    label: 'dust',
    severity: 'info',
    description: 'Incoming transfer below the dust threshold (1 USDT/USDC, 0.0001 ETH, 1 TRX).',
  },
};

const FLAG_PRIORITY = [
  'counterfeit_token',
  'sent_to_lookalike',
  'counterparty_listed',
  'failed',
  'lookalike',
  'zero_value',
  'dust',
];

export function primaryFlagForTx(flags: string[]): FlagMeta | null {
  if (!flags || flags.length === 0) return null;
  if (
    flags.includes('lookalike') &&
    (flags.includes('zero_value') || flags.includes('dust') || flags.includes('counterfeit_token'))
  ) {
    return {
      id: 'address_poisoning',
      label: 'address poisoning',
      severity: 'warning',
      description: FLAG_META.lookalike.description,
    };
  }
  for (const key of FLAG_PRIORITY) {
    if (flags.includes(key)) {
      return FLAG_META[key];
    }
  }
  const f = flags[0];
  return (
    FLAG_META[f] ?? {
      id: f,
      label: f.replace(/_/g, ' '),
      severity: 'info',
      description: f,
    }
  );
}

export function flagBadgeClass(severity: 'critical' | 'warning' | 'info'): string {
  switch (severity) {
    case 'critical':
      return 'bg-red-500/15 text-red-300 border-red-500/30';
    case 'warning':
      return 'bg-amber-500/15 text-amber-300 border-amber-500/30';
    case 'info':
      return 'bg-zinc-800 text-zinc-400 border-zinc-700';
  }
}

export function shortAddr(addr: string): string {
  if (!addr || addr.length <= 12) return addr;
  return `${addr.slice(0, 6)}…${addr.slice(-4)}`;
}

const VALID_ASSETS = new Set<PaymentAsset>(['USDT', 'USDC', 'ETH', 'TRX']);
const VALID_NETWORKS = new Set<PaymentNetwork>(['tron', 'eth', 'arb', 'op', 'base']);

export function parsePaymentHash(): {
  hasPayHash: boolean;
  asset: PaymentAsset;
  network: PaymentNetwork;
  address: string;
} {
  const def = {
    hasPayHash: false,
    asset: 'USDT' as PaymentAsset,
    network: 'tron' as PaymentNetwork,
    address: '',
  };
  if (typeof window === 'undefined') return def;
  const raw = window.location.hash.replace(/^#/, '');
  if (!raw.startsWith('pay')) return def;
  const qIdx = raw.indexOf('?');
  const qs = qIdx >= 0 ? raw.slice(qIdx + 1) : '';
  const sp = new URLSearchParams(qs);
  const rawAsset = (sp.get('asset') ?? 'USDT').toUpperCase() as PaymentAsset;
  const asset: PaymentAsset = VALID_ASSETS.has(rawAsset) ? rawAsset : 'USDT';
  const supportedNets = PAY_ASSET_NETWORKS[asset];
  const rawNet = (sp.get('network') ?? supportedNets[0]).toLowerCase() as PaymentNetwork;
  const network: PaymentNetwork =
    VALID_NETWORKS.has(rawNet) && supportedNets.includes(rawNet) ? rawNet : supportedNets[0];
  const address = (sp.get('address') ?? '').trim();
  return {
    hasPayHash: true,
    asset,
    network,
    address,
  };
}

// Writes asset/network/address into URL hash only (never localStorage; spec Task 11).
export function writePaymentHash(asset: PaymentAsset, network: PaymentNetwork, address: string): void {
  if (typeof window === 'undefined') return;
  const sp = new URLSearchParams();
  sp.set('asset', asset);
  sp.set('network', network);
  if (address.trim()) {
    sp.set('address', address.trim());
  }
  const nextHash = `#pay?${sp.toString()}`;
  if (window.location.hash !== nextHash) {
    window.history.replaceState(null, '', `${window.location.pathname}${window.location.search}${nextHash}`);
  }
}
