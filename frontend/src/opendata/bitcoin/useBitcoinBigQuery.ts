import { useCallback, useState } from 'react';
import type { BtcMetaData } from './types';

const STORAGE_KEY = 'biglens.bitcoin.showBigQuery';

function readStoredPreference(): boolean | null {
  if (typeof window === 'undefined') return null;
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (raw === 'true') return true;
    if (raw === 'false') return false;
  } catch {
    // ignore storage errors
  }
  return null;
}

export interface BitcoinBigQueryControl {
  show: boolean;
  available: boolean;
  jobsEnabled: boolean;
  reason: string;
  toggle: () => void;
}

export function useBitcoinBigQuery(meta: BtcMetaData | null): BitcoinBigQueryControl {
  const [pref, setPref] = useState<boolean | null>(() => readStoredPreference());

  const available = Boolean(meta?.bigquery.data_available);
  const jobsEnabled = Boolean(meta?.bigquery.jobs);
  const show = available ? (pref ?? true) : false;

  const firstDay = meta?.bigquery.costs.first_day_usd ?? 1.49;
  const steadyMonth = meta?.bigquery.costs.steady_month_usd ?? 2.88;
  const reason = available
    ? ''
    : `Operator opt-in required: set bitcoin.bigquery.jobs: true in conf.yaml (≈ $${firstDay.toFixed(2)} first day, ≈ $${steadyMonth.toFixed(2)}/month).`;

  const toggle = useCallback(() => {
    if (!available) return;
    setPref(prev => {
      const next = !(prev ?? true);
      try {
        window.localStorage.setItem(STORAGE_KEY, String(next));
      } catch {
        // ignore storage errors
      }
      return next;
    });
  }, [available]);

  return { show, available, jobsEnabled, reason, toggle };
}
