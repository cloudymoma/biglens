// BigQuery list prices (2025/26). Default constants reflect the US / EU
// multi-region list prices; getRegionPricing(location) resolves regional
// overrides for storage, on-demand $/TiB, and Editions PAYG $/slot-hour.

export const ON_DEMAND_PER_TIB = 6.25;

// Storage, $/GiB/month. Under the physical billing model, time-travel and
// fail-safe bytes bill at the ACTIVE physical rate. Note that BigQuery's
// active_physical_bytes column already includes time-travel bytes.
export const STORAGE_RATES = {
  activeLogical: 0.02,
  longTermLogical: 0.01,
  activePhysical: 0.04,
  longTermPhysical: 0.02,
};

// Editions pay-as-you-go, $/slot-hour.
export const SLOT_HOUR_RATES = {
  standard: 0.04,
  enterprise: 0.06,
  enterprise_plus: 0.1,
} as const;

export type Edition = keyof typeof SLOT_HOUR_RATES;

export interface RegionPricing {
  onDemandPerTiB: number;
  storageRates: {
    activeLogical: number;
    longTermLogical: number;
    activePhysical: number;
    longTermPhysical: number;
  };
  slotHourRates: Record<Edition, number>;
}

const DEFAULT_PRICING: RegionPricing = {
  onDemandPerTiB: ON_DEMAND_PER_TIB,
  storageRates: STORAGE_RATES,
  slotHourRates: { ...SLOT_HOUR_RATES },
};

const REGIONAL_PRICING: Record<string, RegionPricing> = {
  us: DEFAULT_PRICING,
  eu: {
    onDemandPerTiB: 6.25,
    storageRates: { activeLogical: 0.02, longTermLogical: 0.01, activePhysical: 0.044, longTermPhysical: 0.022 },
    slotHourRates: { standard: 0.044, enterprise: 0.066, enterprise_plus: 0.11 },
  },
  'us-central1': {
    onDemandPerTiB: 6.25,
    storageRates: { activeLogical: 0.023, longTermLogical: 0.016, activePhysical: 0.04, longTermPhysical: 0.02 },
    slotHourRates: { standard: 0.04, enterprise: 0.06, enterprise_plus: 0.1 },
  },
  'us-west1': {
    onDemandPerTiB: 6.25,
    storageRates: { activeLogical: 0.02, longTermLogical: 0.01, activePhysical: 0.04, longTermPhysical: 0.02 },
    slotHourRates: { standard: 0.04, enterprise: 0.06, enterprise_plus: 0.1 },
  },
  'us-east1': {
    onDemandPerTiB: 6.25,
    storageRates: { activeLogical: 0.023, longTermLogical: 0.016, activePhysical: 0.04, longTermPhysical: 0.02 },
    slotHourRates: { standard: 0.04, enterprise: 0.06, enterprise_plus: 0.1 },
  },
  'us-east4': {
    onDemandPerTiB: 6.25,
    storageRates: { activeLogical: 0.023, longTermLogical: 0.016, activePhysical: 0.05, longTermPhysical: 0.025 },
    slotHourRates: { standard: 0.04, enterprise: 0.06, enterprise_plus: 0.1 },
  },
  'europe-west1': {
    onDemandPerTiB: 6.25,
    storageRates: { activeLogical: 0.02, longTermLogical: 0.01, activePhysical: 0.04, longTermPhysical: 0.02 },
    slotHourRates: { standard: 0.04, enterprise: 0.06, enterprise_plus: 0.1 },
  },
  'europe-west2': {
    onDemandPerTiB: 7.8125,
    storageRates: { activeLogical: 0.023, longTermLogical: 0.016, activePhysical: 0.052, longTermPhysical: 0.026 },
    slotHourRates: { standard: 0.052, enterprise: 0.078, enterprise_plus: 0.13 },
  },
  'europe-west3': {
    onDemandPerTiB: 7.8125,
    storageRates: { activeLogical: 0.023, longTermLogical: 0.016, activePhysical: 0.052, longTermPhysical: 0.026 },
    slotHourRates: { standard: 0.052, enterprise: 0.078, enterprise_plus: 0.13 },
  },
  'europe-west4': {
    onDemandPerTiB: 6.25,
    storageRates: { activeLogical: 0.023, longTermLogical: 0.016, activePhysical: 0.044, longTermPhysical: 0.022 },
    slotHourRates: { standard: 0.044, enterprise: 0.066, enterprise_plus: 0.11 },
  },
  'asia-northeast1': {
    onDemandPerTiB: 7.5,
    storageRates: { activeLogical: 0.023, longTermLogical: 0.016, activePhysical: 0.052, longTermPhysical: 0.026 },
    slotHourRates: { standard: 0.051, enterprise: 0.078, enterprise_plus: 0.13 },
  },
  'asia-southeast1': {
    onDemandPerTiB: 8.4375,
    storageRates: { activeLogical: 0.02, longTermLogical: 0.01, activePhysical: 0.046, longTermPhysical: 0.023 },
    slotHourRates: { standard: 0.049, enterprise: 0.075, enterprise_plus: 0.125 },
  },
  'australia-southeast1': {
    onDemandPerTiB: 7.8125,
    storageRates: { activeLogical: 0.023, longTermLogical: 0.016, activePhysical: 0.052, longTermPhysical: 0.026 },
    slotHourRates: { standard: 0.052, enterprise: 0.078, enterprise_plus: 0.13 },
  },
  'southamerica-east1': {
    onDemandPerTiB: 11.25,
    storageRates: { activeLogical: 0.023, longTermLogical: 0.016, activePhysical: 0.07, longTermPhysical: 0.035 },
    slotHourRates: { standard: 0.062, enterprise: 0.096, enterprise_plus: 0.16 },
  },
};

export function getRegionPricing(location?: string): RegionPricing {
  if (!location) return DEFAULT_PRICING;
  const key = location.trim().toLowerCase().replace(/^region-/, '');
  return REGIONAL_PRICING[key] ?? DEFAULT_PRICING;
}

export const EDITION_LABELS: Record<Edition, string> = {
  standard: 'Standard',
  enterprise: 'Enterprise',
  enterprise_plus: 'Enterprise Plus',
};

const GIB = 1024 ** 3;
export const TIB = 1024 ** 4;

// Monthly logical-billing cost for a dataset, in USD.
export function logicalCostUSD(activeLogical: number, longTermLogical: number, location?: string): number {
  const rates = getRegionPricing(location).storageRates;
  return (activeLogical / GIB) * rates.activeLogical
    + (longTermLogical / GIB) * rates.longTermLogical;
}

// Monthly physical-billing cost in USD. activePhysical already includes
// time-travel bytes; fail-safe bytes are added at the active rate.
export function physicalCostUSD(activePhysical: number, longTermPhysical: number, failSafe: number, location?: string): number {
  const rates = getRegionPricing(location).storageRates;
  return ((activePhysical + failSafe) / GIB) * rates.activePhysical
    + (longTermPhysical / GIB) * rates.longTermPhysical;
}
