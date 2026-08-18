// Shared chart styling and loading skeleton for the SEM Insights widgets.

export const CHART_TOOLTIP = {
  backgroundColor: 'rgba(17,17,20,0.95)',
  borderColor: '#27272a',
  textStyle: { color: '#e4e4e7', fontSize: 12 },
};
export const AXIS_LABEL = { color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace' };
export const SPLIT_LINE = { lineStyle: { color: '#1f1f23', type: 'dashed' } };

export function LoadingPulse({ count = 3 }: { count?: number }) {
  return (
    <div className="space-y-4">
      {Array.from({ length: count }, (_, i) => (
        <div key={i} className="h-32 rounded-2xl animate-pulse" style={{ background: '#111114' }} />
      ))}
    </div>
  );
}
