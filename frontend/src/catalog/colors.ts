// Deterministic color mapping for OKF concept types. Known catalog types get
// fixed colors; unknown types get a stable hashed palette color so the legend
// and graph stay consistent across renders.

const TYPE_COLORS: Record<string, string> = {
  'BigQuery Table': '#38bdf8',
  'BigQuery View': '#22d3ee',
  'BigQuery Dataset': '#0ea5e9',
  'BigQuery Model': '#818cf8',
  'Glossary Term': '#c084fc',
  'Glossary Category': '#a78bfa',
  Metric: '#fbbf24',
  Untyped: '#71717a',
};

const PALETTE = [
  '#4ade80', '#fb7185', '#f472b6', '#34d399',
  '#f59e0b', '#60a5fa', '#e879f9', '#2dd4bf',
];

// Edge kinds carry distinct colors so containment, lineage, and glossary
// definition links are visually distinguishable in the graph and legend.
export const EDGE_COLORS: Record<string, string> = {
  containment: 'rgba(113,113,122,0.35)',
  lineage: 'rgba(34,211,238,0.45)',
  definition: 'rgba(192,132,252,0.6)',
};

export function colorForEdge(kind?: string): string {
  return EDGE_COLORS[kind ?? ''] ?? 'rgba(113,113,122,0.3)';
}

export function colorForType(type: string): string {
  const t = type || 'Untyped';
  if (TYPE_COLORS[t]) return TYPE_COLORS[t];
  let h = 0;
  for (let i = 0; i < t.length; i++) h = (h * 31 + t.charCodeAt(i)) >>> 0;
  return PALETTE[h % PALETTE.length];
}
