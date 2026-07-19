import { useEffect, useState } from 'react';
import { Search, Info, Pencil, Trash2, Save, Plus } from 'lucide-react';
import type { Concept, ConceptDetail, GraphNode, CatalogTypeCount } from '../types';
import { colorForType } from './colors';

export type CatalogTab = 'search' | 'details' | 'edit';

interface Props {
  active: CatalogTab;
  onTabChange: (t: CatalogTab) => void;

  // Search
  query: string;
  onQuery: (q: string) => void;
  typeFilter: string;
  onTypeFilter: (t: string) => void;
  types: CatalogTypeCount[];
  results: GraphNode[];
  onSelect: (id: string) => void;

  // Details
  detail: ConceptDetail | null;

  // Edit
  onSave: (c: Concept) => void;
  onDelete: (id: string) => void;
  saving: boolean;
  onNew: () => void;
}

const TABS: { id: CatalogTab; label: string; icon: React.ReactNode }[] = [
  { id: 'search', label: 'Search', icon: <Search size={13} /> },
  { id: 'details', label: 'Details', icon: <Info size={13} /> },
  { id: 'edit', label: 'Edit', icon: <Pencil size={13} /> },
];

export default function BottomTabs(p: Props) {
  return (
    <div className="rounded-2xl border border-zinc-800/50 flex flex-col overflow-hidden" style={{ background: '#111114' }}>
      <div className="flex items-center border-b border-zinc-800/50 px-2">
        {TABS.map(t => (
          <button
            key={t.id}
            onClick={() => p.onTabChange(t.id)}
            className={`flex items-center gap-1.5 px-4 py-2.5 text-xs font-medium cursor-pointer transition-all border-b-2 -mb-px ${
              p.active === t.id
                ? 'text-cyan-400 border-cyan-400'
                : 'text-zinc-500 border-transparent hover:text-zinc-300'
            }`}
          >
            {t.icon}{t.label}
          </button>
        ))}
      </div>

      <div className="p-4 max-h-[320px] overflow-y-auto">
        {p.active === 'search' && <SearchTab {...p} />}
        {p.active === 'details' && <DetailsTab detail={p.detail} onSelect={p.onSelect} />}
        {p.active === 'edit' && (
          <EditTab detail={p.detail} onSave={p.onSave} onDelete={p.onDelete} saving={p.saving} onNew={p.onNew} />
        )}
      </div>
    </div>
  );
}

function SearchTab(p: Props) {
  return (
    <div>
      <div className="flex items-center gap-2 mb-3">
        <div className="flex-1 flex items-center gap-2 px-3 py-2 rounded-lg border border-zinc-800/50" style={{ background: '#09090b' }}>
          <Search size={13} className="text-zinc-600" />
          <input
            value={p.query}
            onChange={e => p.onQuery(e.target.value)}
            placeholder="Search concepts by name, type, tag..."
            className="flex-1 text-xs text-zinc-300 bg-transparent outline-none placeholder:text-zinc-700"
          />
        </div>
        <select
          value={p.typeFilter}
          onChange={e => p.onTypeFilter(e.target.value)}
          className="text-xs text-zinc-400 rounded-lg px-3 py-2 outline-none border border-zinc-800/50 cursor-pointer"
          style={{ background: '#09090b' }}
        >
          <option value="">All types</option>
          {p.types.map(t => (
            <option key={t.type} value={t.type}>{t.type} ({t.count})</option>
          ))}
        </select>
      </div>

      {p.results.length === 0 ? (
        <p className="text-xs text-zinc-600 py-6 text-center">No matching concepts</p>
      ) : (
        <div className="flex flex-col gap-1">
          {p.results.map(n => (
            <button
              key={n.id}
              onClick={() => p.onSelect(n.id)}
              className="flex items-center gap-2.5 px-3 py-2 rounded-lg text-left cursor-pointer transition-colors hover:bg-zinc-800/40"
            >
              <span className="w-2.5 h-2.5 rounded-full shrink-0" style={{ background: colorForType(n.type) }} />
              <span className="text-xs text-zinc-200 truncate">{n.title || n.id}</span>
              <span className="ml-auto text-[10px] text-zinc-600 shrink-0">{n.type || 'Untyped'}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function DetailsTab({ detail, onSelect }: { detail: ConceptDetail | null; onSelect: (id: string) => void }) {
  if (!detail) return <p className="text-xs text-zinc-600 py-6 text-center">Select a node to see details</p>;
  const c = detail.concept;
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <span className="w-3 h-3 rounded-full" style={{ background: colorForType(c.type) }} />
        <h3 className="text-sm font-semibold text-white">{c.title || c.id}</h3>
        <span className="text-[10px] px-2 py-0.5 rounded-full border border-zinc-700/50 text-zinc-400">{c.type || 'Untyped'}</span>
      </div>
      <p className="text-[11px] font-mono text-zinc-600">{c.id}</p>
      {c.description && <p className="text-xs text-zinc-400">{c.description}</p>}
      {c.resource && (
        <p className="text-[11px] text-zinc-500 font-mono break-all">
          <span className="text-zinc-600">resource: </span>{c.resource}
        </p>
      )}
      {c.tags && c.tags.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {c.tags.map(t => (
            <span key={t} className="text-[10px] px-2 py-0.5 rounded-full bg-zinc-800/60 text-zinc-400">{t}</span>
          ))}
        </div>
      )}
      {c.body && (
        <MarkdownRenderer content={c.body} onSelect={onSelect} />
      )}
      {detail.neighbors && detail.neighbors.length > 0 && (
        <div>
          <p className="text-[10px] font-semibold text-zinc-600 uppercase tracking-wider mb-2">Connections ({detail.neighbors.length})</p>
          <div className="flex flex-wrap gap-1.5">
            {detail.neighbors.map(n => (
              <button
                key={n.id}
                onClick={() => onSelect(n.id)}
                className="flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-[11px] cursor-pointer transition-colors hover:bg-zinc-800/40 border border-zinc-800/50"
              >
                <span className="w-2 h-2 rounded-full" style={{ background: colorForType(n.type) }} />
                <span className="text-zinc-300">{n.title || n.id}</span>
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

const EMPTY: Concept = {
  id: '', type: '', title: '', description: '', resource: '',
  tags: [], timestamp: '', body: '', links: [],
};

function EditTab({ detail, onSave, onDelete, saving, onNew }: {
  detail: ConceptDetail | null;
  onSave: (c: Concept) => void;
  onDelete: (id: string) => void;
  saving: boolean;
  onNew: () => void;
}) {
  const [form, setForm] = useState<Concept>(EMPTY);
  const [tagsText, setTagsText] = useState('');
  const isNew = !detail;

  useEffect(() => {
    const c = detail?.concept ?? EMPTY;
    setForm(c);
    setTagsText((c.tags ?? []).join(', '));
  }, [detail]);

  const set = (k: keyof Concept, v: string) => setForm(f => ({ ...f, [k]: v }));

  const submit = () => {
    const tags = tagsText.split(',').map(t => t.trim()).filter(Boolean);
    onSave({ ...form, tags });
  };

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <span className="text-[10px] font-semibold text-zinc-600 uppercase tracking-wider">
          {isNew ? 'New concept' : `Editing: ${form.id}`}
        </span>
        <button onClick={onNew} className="flex items-center gap-1 text-[11px] text-cyan-400 hover:text-cyan-300 cursor-pointer">
          <Plus size={12} /> New
        </button>
      </div>

      <div className="grid grid-cols-2 gap-3">
        <Field label="Concept ID (path)" value={form.id} onChange={v => set('id', v)} placeholder="tables/users" mono disabled={!isNew} />
        <Field label="Type" value={form.type} onChange={v => set('type', v)} placeholder="BigQuery Table" />
      </div>
      <Field label="Title" value={form.title} onChange={v => set('title', v)} placeholder="Users" />
      <Field label="Description" value={form.description} onChange={v => set('description', v)} placeholder="One-line summary" />
      <Field label="Resource URI" value={form.resource} onChange={v => set('resource', v)} placeholder="bigquery:proj.ds.users" mono />
      <Field label="Tags (comma-separated)" value={tagsText} onChange={setTagsText} placeholder="pii, core" />

      <div>
        <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1">Body (markdown — links are edges)</label>
        <textarea
          value={form.body}
          onChange={e => set('body', e.target.value)}
          rows={5}
          placeholder="# Schema&#10;...&#10;&#10;Related: [orders](/tables/orders)"
          className="w-full text-xs text-zinc-300 rounded-lg px-3 py-2 outline-none border border-zinc-800/50 focus:border-cyan-500/30 font-mono resize-y placeholder:text-zinc-700"
          style={{ background: '#09090b' }}
        />
      </div>

      <div className="flex items-center gap-2 pt-1">
        <button
          onClick={submit}
          disabled={saving || !form.id || !form.type}
          className="flex items-center gap-1.5 px-4 py-2 rounded-lg text-xs font-medium cursor-pointer transition-all disabled:opacity-40 disabled:cursor-not-allowed"
          style={{ background: 'linear-gradient(135deg, #0e7490, #164e63)', color: '#e0f2fe' }}
        >
          <Save size={13} /> {saving ? 'Saving...' : 'Save'}
        </button>
        {!isNew && (
          <button
            onClick={() => onDelete(form.id)}
            className="flex items-center gap-1.5 px-4 py-2 rounded-lg text-xs font-medium cursor-pointer transition-all border border-red-500/30 text-red-400 hover:bg-red-500/10"
          >
            <Trash2 size={13} /> Delete
          </button>
        )}
      </div>
    </div>
  );
}

function Field({ label, value, onChange, placeholder, mono, disabled }: {
  label: string; value: string; onChange: (v: string) => void;
  placeholder?: string; mono?: boolean; disabled?: boolean;
}) {
  return (
    <div className={disabled ? 'opacity-50' : ''}>
      <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1">{label}</label>
      <input
        value={value}
        onChange={e => onChange(e.target.value)}
        placeholder={placeholder}
        disabled={disabled}
        className={`w-full text-xs text-zinc-300 rounded-lg px-3 py-2 outline-none border border-zinc-800/50 focus:border-cyan-500/30 placeholder:text-zinc-700 ${mono ? 'font-mono' : ''}`}
        style={{ background: '#09090b' }}
      />
    </div>
  );
}

function MarkdownRenderer({ content, onSelect }: { content: string; onSelect: (id: string) => void }) {
  if (!content) return null;

  const lines = content.split('\n');
  const elements: React.ReactNode[] = [];
  let currentList: React.ReactNode[] = [];
  let keyIndex = 0;

  const flushList = () => {
    if (currentList.length > 0) {
      elements.push(
        <ul key={`ul-${keyIndex++}`} className="list-disc list-inside space-y-1 my-2 text-xs text-zinc-300">
          {currentList}
        </ul>
      );
      currentList = [];
    }
  };

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const trimmed = line.trim();

    if (!trimmed) {
      flushList();
      continue;
    }

    if (trimmed.startsWith('# ')) {
      flushList();
      elements.push(
        <h3 key={`h-${keyIndex++}`} className="text-xs font-semibold text-cyan-400 uppercase tracking-wider mt-4 mb-2 pb-1 border-b border-zinc-800/60">
          {trimmed.substring(2)}
        </h3>
      );
    } else if (trimmed.startsWith('## ')) {
      flushList();
      elements.push(
        <h4 key={`h-${keyIndex++}`} className="text-xs font-semibold text-zinc-200 mt-3 mb-1.5">
          {trimmed.substring(3)}
        </h4>
      );
    } else if (trimmed.startsWith('- ') || trimmed.startsWith('* ')) {
      const itemContent = line.substring(line.indexOf('- ') + 2);
      const leadingSpaces = line.search(/\S/);
      const indentClass = leadingSpaces > 0 ? 'ml-4' : '';
      currentList.push(
        <li key={`li-${keyIndex++}`} className={`text-[11px] text-zinc-300 leading-relaxed ${indentClass}`}>
          {renderFormattedInline(itemContent, onSelect)}
        </li>
      );
    } else {
      flushList();
      elements.push(
        <p key={`p-${keyIndex++}`} className="text-[11px] text-zinc-300 leading-relaxed my-1">
          {renderFormattedInline(trimmed, onSelect)}
        </p>
      );
    }
  }
  flushList();

  return (
    <div className="p-3 rounded-lg border border-zinc-800/50 space-y-1" style={{ background: '#09090b' }}>
      {elements}
    </div>
  );
}

function renderFormattedInline(text: string, onSelect: (id: string) => void): React.ReactNode[] {
  const parts: React.ReactNode[] = [];
  const regex = /\[([^\]]+)\]\(([^)]+)\)|`([^`]+)`/g;
  let lastIndex = 0;
  let match: RegExpExecArray | null;

  while ((match = regex.exec(text)) !== null) {
    if (match.index > lastIndex) {
      parts.push(text.substring(lastIndex, match.index));
    }

    if (match[1] !== undefined && match[2] !== undefined) {
      const label = match[1];
      const target = match[2].replace(/^\//, '').replace(/\.md$/, '');
      const isInternal = match[2].startsWith('/') || !match[2].includes('://');
      if (isInternal) {
        parts.push(
          <button
            key={match.index}
            onClick={() => onSelect(target)}
            className="text-cyan-400 hover:text-cyan-300 underline font-mono text-[11px] cursor-pointer inline-flex items-center gap-0.5 px-0.5"
          >
            {label}
          </button>
        );
      } else {
        parts.push(
          <a
            key={match.index}
            href={match[2]}
            target="_blank"
            rel="noreferrer"
            className="text-cyan-400 hover:underline"
          >
            {label}
          </a>
        );
      }
    } else if (match[3] !== undefined) {
      parts.push(
        <code key={match.index} className="font-mono text-[11px] px-1 py-0.5 rounded bg-zinc-800/60 text-cyan-300">
          {match[3]}
        </code>
      );
    }

    lastIndex = regex.lastIndex;
  }

  if (lastIndex < text.length) {
    parts.push(text.substring(lastIndex));
  }

  return parts;
}
