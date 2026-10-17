import { useEffect, useState } from 'react';

export interface PollingResult<T> {
  data: T | null;
  loading: boolean;
  error: string;
}

export function usePolling<T>(
  fetcher: () => Promise<T>,
  intervalMs: number,
  key: string,
  enabled = true,
): PollingResult<T> {
  const [resolved, setResolved] = useState<{
    key: string;
    data: T | null;
    error: string;
  }>({ key: '', data: null, error: '' });

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;

    const run = () => {
      if (typeof document !== 'undefined' && document.visibilityState === 'hidden') {
        return;
      }
      fetcher()
        .then(d => {
          if (!cancelled) {
            setResolved(prev => ({
              key,
              data: d,
              error: '',
              ...(prev.key === key ? {} : {}),
            }));
          }
        })
        .catch((e: { response?: { data?: unknown }; message?: string }) => {
          if (!cancelled) {
            const msg =
              typeof e.response?.data === 'string' && e.response.data.trim()
                ? e.response.data.trim()
                : e.message || 'request failed';
            setResolved(prev => ({
              key,
              data: prev.key === key ? prev.data : null,
              error: msg,
            }));
          }
        });
    };

    run();
    if (intervalMs <= 0) {
      return () => {
        cancelled = true;
      };
    }
    const id = window.setInterval(run, intervalMs);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
  }, [fetcher, intervalMs, key, enabled]);

  const isCurrent = resolved.key === key;
  return {
    data: isCurrent ? resolved.data : null,
    loading: enabled && !isCurrent,
    error: isCurrent ? resolved.error : '',
  };
}
