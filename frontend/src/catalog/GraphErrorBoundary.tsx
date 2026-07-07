import { Component, type ReactNode } from 'react';
import { AlertTriangle } from 'lucide-react';

interface Props { children: ReactNode }
interface State { error: Error | null }

// Catches render/runtime errors from the graph renderers (notably the WebGL
// 3D view) so a failure shows the actual message instead of a blank canvas or
// a crashed app. Remount via a `key` (e.g. the render mode) to reset.
export default class GraphErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  render() {
    if (this.state.error) {
      return (
        <div className="absolute inset-0 flex flex-col items-center justify-center gap-3 text-center px-6">
          <AlertTriangle size={28} className="text-amber-400" />
          <p className="text-sm font-medium text-zinc-300">Graph failed to render</p>
          <p className="text-[11px] text-red-400/80 font-mono max-w-lg break-words">
            {this.state.error.message || String(this.state.error)}
          </p>
          <p className="text-[11px] text-zinc-600">
            If this is the 3D view, your browser/GPU may not support WebGL — the 2D view should still work.
          </p>
        </div>
      );
    }
    return this.props.children;
  }
}
