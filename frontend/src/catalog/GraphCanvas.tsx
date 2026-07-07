import { useEffect, useMemo, useRef, useState } from 'react';
import ForceGraph2D from 'react-force-graph-2d';
import ForceGraph3D from 'react-force-graph-3d';
import SpriteText from 'three-spritetext';
import type { CatalogGraph } from '../types';
import { colorForType } from './colors';

interface Props {
  graph: CatalogGraph;
  mode: '2d' | '3d';
  selectedId: string | null;
  onSelect: (id: string) => void;
}

// useElementSize tracks a container's pixel size so the force graph fills it
// (react-force-graph defaults to window size and would otherwise overflow).
function useElementSize() {
  const ref = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ width: 0, height: 0 });
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(entries => {
      const { width, height } = entries[0].contentRect;
      setSize({ width, height });
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return { ref, size };
}

export default function GraphCanvas({ graph, mode, selectedId, onSelect }: Props) {
  const { ref, size } = useElementSize();

  // Clone into the {nodes, links} shape the lib mutates internally.
  const data = useMemo(() => ({
    nodes: graph.nodes.map(n => ({ ...n })),
    links: graph.edges.map(e => ({ source: e.source, target: e.target })),
  }), [graph]);

  const common = {
    graphData: data,
    width: size.width,
    height: size.height,
    backgroundColor: '#09090b',
    nodeId: 'id',
    nodeRelSize: 5,
    nodeLabel: (n: any) => `${n.title || n.id} · ${n.type || 'Untyped'}`,
    nodeColor: (n: any) => (n.id === selectedId ? '#ffffff' : colorForType(n.type)),
    linkColor: () => 'rgba(113,113,122,0.3)',
    linkDirectionalParticles: 0,
    onNodeClick: (n: any) => onSelect(n.id),
    cooldownTicks: 120,
  };

  return (
    <div ref={ref} className="w-full h-full">
      {size.width > 0 && (
        mode === '3d' ? (
          <ForceGraph3D
            {...common}
            nodeThreeObject={(n: any) => {
              const s = new SpriteText(n.title || n.id);
              s.color = n.id === selectedId ? '#ffffff' : colorForType(n.type);
              s.textHeight = 4;
              return s;
            }}
            nodeThreeObjectExtend
          />
        ) : (
          <ForceGraph2D
            {...common}
            nodeCanvasObjectMode={() => 'after'}
            nodeCanvasObject={(n: any, ctx: CanvasRenderingContext2D, scale: number) => {
              if (scale < 1.2) return; // only label when zoomed in enough
              const label = n.title || n.id;
              ctx.font = `${11 / scale}px JetBrains Mono, monospace`;
              ctx.fillStyle = '#a1a1aa';
              ctx.textAlign = 'center';
              ctx.fillText(label, n.x, n.y + 9);
            }}
          />
        )
      )}
    </div>
  );
}
