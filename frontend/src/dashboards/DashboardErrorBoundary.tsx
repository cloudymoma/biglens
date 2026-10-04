import { Component, type ReactNode } from 'react';
import { ErrorBanner } from './shared';

interface Props { children: ReactNode }
interface State { error: Error | null }

// Contains a render/runtime error to the dashboard that threw, so one bad
// payload shows an error banner instead of unmounting the whole app (e.g. a
// null list from an API reaching `.map`). Remount via a `key` to reset.
export default class DashboardErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  render() {
    if (this.state.error) {
      return <ErrorBanner message={this.state.error.message || String(this.state.error)} />;
    }
    return this.props.children;
  }
}
