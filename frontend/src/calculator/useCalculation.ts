import { useEffect, useState } from 'react';
import axios from 'axios';

// Posts the inputs to the backend whenever they change. Typing is debounced
// and stale in-flight requests are aborted, so the estimate on screen always
// matches the latest inputs. The previous estimate stays visible while the
// next one loads.
export function useCalculation<Req, Res>(
  req: Req,
  post: (req: Req, signal: AbortSignal) => Promise<Res>,
  delayMs = 150,
) {
  const [est, setEst] = useState<Res | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Serialising the request makes the effect fire on value changes only.
  const key = JSON.stringify(req);

  useEffect(() => {
    const ctrl = new AbortController();
    const timer = setTimeout(() => {
      setLoading(true);
      post(JSON.parse(key) as Req, ctrl.signal)
        .then(res => { setEst(res); setError(null); })
        .catch(err => { if (!axios.isCancel(err)) setError(errorMessage(err)); })
        .finally(() => { if (!ctrl.signal.aborted) setLoading(false); });
    }, delayMs);
    return () => { clearTimeout(timer); ctrl.abort(); };
  }, [key, post, delayMs]);

  return { est, loading, error };
}

function errorMessage(err: unknown): string {
  if (axios.isAxiosError(err) && typeof err.response?.data === 'string' && err.response.data.trim()) {
    return err.response.data.trim();
  }
  return err instanceof Error ? err.message : String(err);
}
