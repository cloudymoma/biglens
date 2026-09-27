import { useState } from 'react';
import { ExternalLink } from 'lucide-react';
import type { AddressRiskKeyInfo } from '../../types';
import { deleteAddressRiskKey, saveAddressRiskKey } from '../../api';
import { fmtNum } from './shared';
import { ETHERSCAN_HELP_URL, ETHERSCAN_SIGNUP_URL, ETHERSCAN_TERMS_NOTE } from './addressRiskTools';

// writeError answers text/plain; anything else (proxy HTML/JSON) must not be
// rendered as a React child.
function errorText(e: { response?: { data?: unknown }; message: string }): string {
  const d = e.response?.data;
  return typeof d === 'string' && d ? d : e.message;
}

// Inline Etherscan key form (no modal: the codebase has none). The key is sent
// once, validated server-side, and only a 4-character hint ever comes back.
export default function EtherscanKeyPanel({ info, onChange }: {
  info: AddressRiskKeyInfo | null;
  onChange: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [key, setKey] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [confirmRemove, setConfirmRemove] = useState(false);

  const save = () => {
    setBusy(true);
    setError('');
    saveAddressRiskKey(key.trim())
      .then(() => {
        setKey('');
        setEditing(false);
        onChange();
      })
      .catch(e => setError(errorText(e)))
      .finally(() => setBusy(false));
  };

  const remove = () => {
    setBusy(true);
    setError('');
    deleteAddressRiskKey()
      .then(() => {
        setConfirmRemove(false);
        onChange();
      })
      .catch(e => setError(errorText(e)))
      .finally(() => setBusy(false));
  };

  const link = 'text-zinc-300 hover:text-white inline-flex items-center gap-1';
  const button = 'text-zinc-300 hover:text-white underline decoration-zinc-700 disabled:opacity-50';

  return (
    <div className="mt-3 rounded-lg border border-zinc-800 p-3 text-[11px] text-zinc-500 space-y-2">
      {info?.configured && !editing ? (
        <p>
          Etherscan key <span className="font-mono text-zinc-300">…{info.hint}</span>
          {info.credits_available !== undefined && <> · {fmtNum(info.credits_available)} credits left</>}
          {' · '}
          <button type="button" className={button} onClick={() => setEditing(true)} disabled={busy}>Replace</button>
          {' · '}
          {confirmRemove ? (
            <>
              Remove the key?{' '}
              <button type="button" className={button} onClick={remove} disabled={busy}>Yes, remove</button>{' '}
              <button type="button" className={button} onClick={() => setConfirmRemove(false)} disabled={busy}>Cancel</button>
            </>
          ) : (
            <button type="button" className={button} onClick={() => setConfirmRemove(true)} disabled={busy}>Remove</button>
          )}
        </p>
      ) : (
        <>
          <p>
            Association analysis (transfers with listed addresses) needs a free Etherscan API key.{' '}
            <a href={ETHERSCAN_SIGNUP_URL} target="_blank" rel="noopener noreferrer" className={link}>Get a free key <ExternalLink size={10} /></a>
            {' · '}
            <a href={ETHERSCAN_HELP_URL} target="_blank" rel="noopener noreferrer" className={link}>How? <ExternalLink size={10} /></a>
          </p>
          <p>Free, sign-up required; 3 calls/s, 100k/day; new keys take a few minutes.</p>
          <form className="flex gap-2" onSubmit={e => { e.preventDefault(); save(); }}>
            <input
              type="password"
              autoComplete="off"
              value={key}
              onChange={e => setKey(e.target.value)}
              placeholder="Etherscan API key"
              aria-label="Etherscan API key"
              className="flex-1 rounded-md bg-zinc-900 border border-zinc-800 px-2 py-1 font-mono text-zinc-200 focus:outline-none focus:border-zinc-600"
            />
            <button type="submit" disabled={busy || key.trim() === ''} className="px-3 py-1 rounded-md bg-zinc-800 text-white disabled:opacity-50">
              {busy ? 'Checking…' : 'Save'}
            </button>
            {editing && (
              <button type="button" onClick={() => { setEditing(false); setKey(''); setError(''); }} className={button}>Cancel</button>
            )}
          </form>
        </>
      )}
      {error && <p className="text-orange-400">{error}</p>}
      <p className="text-zinc-600">{ETHERSCAN_TERMS_NOTE}</p>
    </div>
  );
}
