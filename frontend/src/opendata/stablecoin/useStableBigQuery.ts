import { useCallback, useState } from 'react';
import type { StableMeta } from './types';

export const STORAGE_KEY = 'biglens.stablecoin.showBigQuery';

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

export function resolveStableBigQueryShow(pref: boolean | null, available: boolean): boolean {
  return available ? (pref ?? false) : false;
}

export function nextStableBigQueryPreference(prev: boolean | null): boolean {
  return !(prev ?? false);
}

export interface StableBigQueryControl {
  show: boolean;
  available: boolean;
  jobsEnabled: boolean;
  reason: string;
  toggle: () => void;
}

export function useStableBigQuery(meta: StableMeta | null): StableBigQueryControl {
  const [pref, setPref] = useState<boolean | null>(() => readStoredPreference());

  const available = Boolean(meta?.bigquery.data_available);
  const jobsEnabled = Boolean(meta?.bigquery.jobs);
  const show = resolveStableBigQueryShow(pref, available);

  const reason = available
    ? ''
    : 'Operator opt-in required: set stablecoin.bigquery.jobs: true in conf.yaml (or sync SQLite tables first).';

  const toggle = useCallback(() => {
    if (!available) return;
    setPref(prev => {
      const next = nextStableBigQueryPreference(prev);
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
