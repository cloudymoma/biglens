import React from 'react';
import { AlertCircle, BarChart3 } from 'lucide-react';

export function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}

export function MetricCard({ label, value, icon, detail, accentColor }: {
  label: string; value: string; icon: React.ReactNode; detail: string; accentColor: string;
}) {
  return (
    <div
      className="relative rounded-2xl border border-zinc-800/50 p-5 overflow-hidden group transition-all duration-300 hover:border-zinc-700/60"
      style={{ background: '#111114' }}
    >
      <div className="relative z-10">
        <div className="flex items-center justify-between mb-4">
          <span className="text-xs font-medium text-zinc-500">{label}</span>
          <div
            className="p-1.5 rounded-lg border"
            style={{ color: accentColor, borderColor: `${accentColor}20`, background: `${accentColor}08` }}
          >
            {icon}
          </div>
        </div>
        <p className="text-2xl font-bold text-white font-mono tracking-tight">{value}</p>
        <p className="text-[11px] text-zinc-600 mt-1">{detail}</p>
      </div>
    </div>
  );
}

export function EmptyState({ text }: { text: string }) {
  return (
    <div className="flex flex-col items-center justify-center py-16 text-zinc-600 gap-3">
      <BarChart3 size={28} strokeWidth={1.5} />
      <p className="text-xs font-medium">{text}</p>
    </div>
  );
}

export function ErrorBanner({ message }: { message: string }) {
  return (
    <div className="flex items-center gap-3 p-4 rounded-xl border border-red-500/20 bg-red-500/5 text-red-400">
      <AlertCircle size={18} />
      <div>
        <p className="text-sm font-medium">Failed to load data</p>
        <p className="text-xs text-red-400/70 mt-0.5 font-mono">{message}</p>
      </div>
    </div>
  );
}
