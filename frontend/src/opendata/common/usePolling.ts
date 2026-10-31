import { useEffect, useRef, useState } from 'react';

export interface PollingResult<T> {
  data: T | null;
  loading: boolean;
  error: string;
}

export interface PollingDriverOptions {
  intervalMs: number;
  isHidden: () => boolean;
  subscribeVisibility: (cb: () => void) => () => void;
  setIntervalFn: (cb: () => void, ms: number) => number;
  clearIntervalFn: (id: number) => void;
  onRun: () => void;
}

export function startPollingDriver(opts: PollingDriverOptions): () => void {
  let hasRun = false;

  const tryRun = () => {
    if (opts.isHidden()) {
      return;
    }
    hasRun = true;
    opts.onRun();
  };

  tryRun();

  const unsubscribeVisibility = opts.subscribeVisibility(() => {
    if (!opts.isHidden() && !hasRun) {
      tryRun();
    }
  });

  if (opts.intervalMs <= 0) {
    return () => {
      unsubscribeVisibility();
    };
  }

  const id = opts.setIntervalFn(tryRun, opts.intervalMs);
  return () => {
    unsubscribeVisibility();
    opts.clearIntervalFn(id);
  };
}

export function usePolling<T>(
  fetcher: () => Promise<T>,
  intervalMs: number,
  key: string,
  enabled = true,
): PollingResult<T> {
  const fetcherRef = useRef(fetcher);
  useEffect(() => {
    fetcherRef.current = fetcher;
  });

  const [resolved, setResolved] = useState<{
    key: string;
    data: T | null;
    error: string;
  }>({ key: '', data: null, error: '' });

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;

    const run = () => {
      fetcherRef
        .current()
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

    const cleanupDriver = startPollingDriver({
      intervalMs,
      isHidden: () => typeof document !== 'undefined' && document.visibilityState === 'hidden',
      subscribeVisibility: cb => {
        if (typeof document === 'undefined') {
          return () => {};
        }
        document.addEventListener('visibilitychange', cb);
        return () => document.removeEventListener('visibilitychange', cb);
      },
      setIntervalFn: (cb, ms) => window.setInterval(cb, ms),
      clearIntervalFn: id => window.clearInterval(id),
      onRun: run,
    });

    return () => {
      cancelled = true;
      cleanupDriver();
    };
  }, [intervalMs, key, enabled]);

  const isCurrent = resolved.key === key;
  return {
    data: isCurrent ? resolved.data : null,
    loading: enabled && !isCurrent,
    error: isCurrent ? resolved.error : '',
  };
}
