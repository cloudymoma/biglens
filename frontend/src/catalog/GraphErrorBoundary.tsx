import { Component, type ReactNode } from 'react';
import { AlertTriangle, RefreshCw } from 'lucide-react';

interface Props { children: ReactNode }
interface State { error: Error | null }

function isChunkLoadError(error: Error): boolean {
  const msg = error?.message || String(error);
  return /Failed to fetch dynamically imported module|Importing a module script failed|Unable to preload|error loading dynamically imported module/i.test(msg);
}

// Catches render/runtime errors from the graph renderers (notably WebGL 3D
// failures or stale lazy-chunk 404s after a rebuild) so a failure shows a
// helpful message instead of a blank canvas or crashed app.
export default class GraphErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  render() {
    if (this.state.error) {
      const msg = this.state.error.message || String(this.state.error);
      const staleChunk = isChunkLoadError(this.state.error);
      return (
        <div className="absolute inset-0 flex flex-col items-center justify-center gap-3 text-center px-6">
          <AlertTriangle size={28} className="text-amber-400" />
          <p className="text-sm font-medium text-zinc-300">Graph failed to render</p>
          <p className="text-[11px] text-red-400/80 font-mono max-w-lg break-words">
            {msg}
          </p>
          {staleChunk ? (
            <>
              <p className="text-[11px] text-zinc-500 max-w-md">
                A newer build was deployed since this tab was opened, so the old graph bundle hash is no longer on the server.
              </p>
              <button
                onClick={() => window.location.reload()}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium border border-cyan-500/30 bg-cyan-500/10 text-cyan-300 hover:bg-cyan-500/20 cursor-pointer transition-all"
              >
                <RefreshCw size={12} /> Reload page
              </button>
            </>
          ) : (
            <p className="text-[11px] text-zinc-600">
              If this is the 3D view, your browser/GPU may not support WebGL — the 2D view should still work.
            </p>
          )}
        </div>
      );
    }
    return this.props.children;
  }
}

