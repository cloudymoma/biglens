import { useState } from 'react';
import type { AddressRiskChain, PaymentAsset, PaymentNetwork, PaymentQueryParams } from '../../types';
import { Panel } from './shared';
import {
  detectAddressFamily,
  isValidAddressForChain,
  networksForAssetAndFamily,
  PAY_ASSET_NETWORKS,
  PAY_ASSETS,
  PAY_NETWORKS,
  parsePaymentHash,
  writePaymentHash,
} from './paymentCheck';

const FAMILY_LABELS: Record<'evm' | 'tron' | 'btc', string> = {
  evm: 'an EVM',
  tron: 'a TRON',
  btc: 'a Bitcoin',
};

export default function PaymentCheckTab({
  onInspect,
}: {
  onInspect?: (address: string, chain: AddressRiskChain) => void;
}) {
  const initial = parsePaymentHash();
  const initialValid =
    initial.address && isValidAddressForChain(initial.network, initial.address)
      ? { asset: initial.asset, network: initial.network, address: initial.address }
      : null;

  const [asset, setAsset] = useState<PaymentAsset>(initial.asset);
  const [network, setNetwork] = useState<PaymentNetwork>(initial.network);
  const [input, setInput] = useState<string>(initial.address);
  const [inputError, setInputError] = useState<string>('');
  const [submitted, setSubmitted] = useState<PaymentQueryParams | null>(initialValid);

  const supportedNetworks = PAY_ASSET_NETWORKS[asset];
  const currentNetOpt = PAY_NETWORKS[network];
  const trimmedInput = input.trim();
  const detectedFamily = detectAddressFamily(trimmedInput);
  const familyMismatch =
    detectedFamily !== null && detectedFamily !== currentNetOpt.family ? detectedFamily : null;
  const matchingAssetNetworks =
    familyMismatch !== null ? networksForAssetAndFamily(asset, familyMismatch) : [];

  const selectAsset = (nextAsset: PaymentAsset) => {
    const nets = PAY_ASSET_NETWORKS[nextAsset];
    const nextNet = nets.includes(network) ? network : nets[0];
    setAsset(nextAsset);
    setNetwork(nextNet);
    setInputError('');
    if (trimmedInput && isValidAddressForChain(nextNet, trimmedInput)) {
      writePaymentHash(nextAsset, nextNet, trimmedInput);
      setSubmitted({ asset: nextAsset, network: nextNet, address: trimmedInput });
    } else {
      writePaymentHash(nextAsset, nextNet, '');
      setSubmitted(null);
    }
  };

  const selectNetwork = (nextNet: PaymentNetwork) => {
    setNetwork(nextNet);
    setInputError('');
    if (trimmedInput && isValidAddressForChain(nextNet, trimmedInput)) {
      writePaymentHash(asset, nextNet, trimmedInput);
      setSubmitted({ asset, network: nextNet, address: trimmedInput });
    } else {
      writePaymentHash(asset, nextNet, '');
      setSubmitted(null);
    }
  };

  const submitCheck = (targetNet: PaymentNetwork = network, rawAddr: string = input) => {
    const addr = rawAddr.trim();
    const netOpt = PAY_NETWORKS[targetNet];
    if (!isValidAddressForChain(targetNet, addr)) {
      setInputError(netOpt.formatHint);
      return;
    }
    setInputError('');
    writePaymentHash(asset, targetNet, addr);
    setSubmitted({ asset, network: targetNet, address: addr });
  };

  return (
    <div className="space-y-4">
      <Panel
        title="Payment Check · Receiving Address Verification"
        note="settlement finality · official vs counterfeit token · payer screening"
      >
        <div className="space-y-3">
          {/* Step 1: Asset selector */}
          <div className="flex flex-wrap items-center gap-2">
            <span className="w-16 text-[11px] font-medium text-zinc-400">1. Asset</span>
            <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Payment asset">
              {PAY_ASSETS.map(a =>
                a.enabled ? (
                  <button
                    key={a.id}
                    type="button"
                    onClick={() => selectAsset(a.id as PaymentAsset)}
                    className={`rounded-lg px-2.5 py-1 text-xs font-medium transition-colors ${
                      asset === a.id
                        ? 'bg-cyan-500/20 text-cyan-300 border border-cyan-500/40'
                        : 'bg-zinc-900 text-zinc-400 border border-zinc-800 hover:text-zinc-200'
                    }`}
                  >
                    {a.label}
                  </button>
                ) : (
                  <span
                    key={a.id}
                    title={`${a.label} Payment Check is ${a.note}`}
                    className="rounded-lg px-2.5 py-1 text-xs font-medium bg-zinc-900/50 text-zinc-600 border border-zinc-800/60 cursor-not-allowed select-none"
                  >
                    {a.label}{' '}
                    <span className="text-[10px] font-normal text-zinc-600">({a.note})</span>
                  </span>
                ),
              )}
            </div>
          </div>

          {/* Step 2: Network selector */}
          <div className="flex flex-wrap items-center gap-2">
            <span className="w-16 text-[11px] font-medium text-zinc-400">2. Network</span>
            <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Payment network">
              {supportedNetworks.map(netId => {
                const netOpt = PAY_NETWORKS[netId];
                const isBaseBridgedUsdt = asset === 'USDT' && netId === 'base';
                return (
                  <button
                    key={netId}
                    type="button"
                    onClick={() => selectNetwork(netId)}
                    className={`rounded-lg px-2.5 py-1 text-xs font-medium transition-colors ${
                      network === netId
                        ? 'bg-cyan-500/20 text-cyan-300 border border-cyan-500/40'
                        : 'bg-zinc-900 text-zinc-400 border border-zinc-800 hover:text-zinc-200'
                    }`}
                  >
                    {netOpt.label}
                    {isBaseBridgedUsdt && (
                      <span className="ml-1 text-[10px] text-amber-300">⚠ bridged USDT only</span>
                    )}
                  </button>
                );
              })}
            </div>
          </div>

          {asset === 'USDT' && network === 'base' && (
            <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-1.5 text-xs text-amber-200">
              ⚠ Base has bridged USDT only (<span className="font-mono">0xfde4…9bb2</span>) — Tether has not deployed native USDT on Base, and there is no on-chain freeze function on the bridged contract.
            </div>
          )}

          {/* Step 3: Address input */}
          <form
            onSubmit={e => {
              e.preventDefault();
              submitCheck();
            }}
            className="flex flex-wrap items-center gap-2 pt-1"
          >
            <label htmlFor="payment-check-address" className="w-16 text-[11px] font-medium text-zinc-400">
              3. Address
            </label>
            <input
              id="payment-check-address"
              type="text"
              value={input}
              onChange={e => {
                setInput(e.target.value);
                if (inputError) setInputError('');
              }}
              placeholder={currentNetOpt.placeholder}
              spellCheck={false}
              autoComplete="off"
              className="flex-1 min-w-[260px] rounded-lg border border-zinc-700 bg-zinc-900 px-3 py-1.5 font-mono text-xs text-white placeholder-zinc-600 focus:border-cyan-500 focus:outline-none"
            />
            <button
              type="submit"
              className="rounded-lg bg-cyan-600 px-3.5 py-1.5 text-xs font-medium text-white hover:bg-cyan-500"
            >
              Check payment
            </button>
          </form>

          {familyMismatch && (
            <div className="flex flex-wrap items-center gap-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
              <span>
                This is {FAMILY_LABELS[familyMismatch]} address, while {currentNetOpt.label} expects{' '}
                {FAMILY_LABELS[currentNetOpt.family]} address.
              </span>
              {matchingAssetNetworks.length > 0 ? (
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="text-amber-300/80">Switch network for {asset}:</span>
                  {matchingAssetNetworks.map(netId => (
                    <button
                      key={netId}
                      type="button"
                      onClick={() => {
                        setNetwork(netId);
                        setInputError('');
                        submitCheck(netId, input);
                      }}
                      className="rounded border border-amber-400/40 bg-amber-500/20 px-2 py-0.5 text-[11px] font-medium text-amber-100 hover:bg-amber-500/30"
                    >
                      {PAY_NETWORKS[netId].label}
                    </button>
                  ))}
                </div>
              ) : (
                <span className="text-amber-300/80">
                  {asset} is not supported on {familyMismatch.toUpperCase()} in Payment Check.
                </span>
              )}
            </div>
          )}

          {inputError && <p className="text-xs text-red-400">{inputError}</p>}

          <p className="text-[11px] text-zinc-500">
            Queried addresses are kept only in the URL hash and browser memory (never saved to localStorage or server access logs).
          </p>
        </div>
      </Panel>

      {submitted && (
        <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-zinc-400 font-mono">
          <span>
            Active target: {submitted.asset} on {PAY_NETWORKS[submitted.network].label} · {submitted.address}
          </span>
          {onInspect && (
            <button
              type="button"
              onClick={() => onInspect(submitted.address, submitted.network)}
              className="text-zinc-400 hover:text-zinc-200 underline underline-offset-2 font-sans"
            >
              Open in Address Risk
            </button>
          )}
        </div>
      )}
    </div>
  );
}
