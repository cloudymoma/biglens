// BigQuery US multi-region list prices (2025/26). Single source for every
// tab so a price revision is a one-file change.

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

export const EDITION_LABELS: Record<Edition, string> = {
  standard: 'Standard',
  enterprise: 'Enterprise',
  enterprise_plus: 'Enterprise Plus',
};

const GIB = 1024 ** 3;
export const TIB = 1024 ** 4;

// Monthly logical-billing cost for a dataset, in USD.
export function logicalCostUSD(activeLogical: number, longTermLogical: number): number {
  return (activeLogical / GIB) * STORAGE_RATES.activeLogical
    + (longTermLogical / GIB) * STORAGE_RATES.longTermLogical;
}

// Monthly physical-billing cost in USD. activePhysical already includes
// time-travel bytes; fail-safe bytes are added at the active rate.
export function physicalCostUSD(activePhysical: number, longTermPhysical: number, failSafe: number): number {
  return ((activePhysical + failSafe) / GIB) * STORAGE_RATES.activePhysical
    + (longTermPhysical / GIB) * STORAGE_RATES.longTermPhysical;
}
