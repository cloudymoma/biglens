import { useId } from 'react';

export interface IconProps {
  size?: number;
  className?: string;
}

function normKey(raw: string): string {
  const s = raw.trim().toLowerCase();
  if (s === 'bitcoin' || s === 'btc') return 'btc';
  if (s === 'ethereum' || s === 'eth') return 'eth';
  if (s === 'arbitrum' || s === 'arbitrum one' || s === 'arb') return 'arb';
  if (s === 'optimism' || s === 'op mainnet' || s === 'op') return 'op';
  if (s === 'base') return 'base';
  if (s === 'polygon' || s === 'polygon pos' || s === 'poly' || s === 'matic' || s === 'pol') return 'poly';
  if (s === 'tron' || s === 'trx') return 'tron';
  if (s === 'solana' || s === 'sol') return 'sol';
  if (s.startsWith('usdt') || s.startsWith('usd₮') || s === 'tether') return 'usdt';
  if (s.startsWith('usdc')) return 'usdc';
  return s;
}

export function ChainIcon({
  chain,
  size = 15,
  className = '',
}: IconProps & { chain: string }) {
  return <CryptoGlyph id={normKey(chain)} label={chain} size={size} className={className} />;
}

export function TokenIcon({
  symbol,
  size = 15,
  className = '',
}: IconProps & { symbol: string }) {
  return <CryptoGlyph id={normKey(symbol)} label={symbol} size={size} className={className} />;
}

function CryptoGlyph({
  id,
  label,
  size,
  className,
}: {
  id: string;
  label: string;
  size: number;
  className: string;
}) {
  const uid = useId();
  const baseClass = `inline-block shrink-0 align-middle select-none ${className}`.trim();

  switch (id) {
    case 'btc':
      return (
        <svg
          width={size}
          height={size}
          viewBox="0 0 24 24"
          fill="none"
          aria-hidden="true"
          className={baseClass}
        >
          <circle cx="12" cy="12" r="11" fill="#F7931A" />
          <path
            d="M16.1 10.4c.22-1.45-.89-2.23-2.4-2.75l.49-1.97-1.2-.3-.48 1.92c-.31-.08-.64-.15-.96-.23l.48-1.93-1.2-.3-.49 1.97c-.26-.06-.51-.12-.76-.18l-1.65-.41-.32 1.28s.89.2.87.22c.48.12.57.44.56.7l-.56 2.25c.03.01.08.02.13.05l-.13-.03-.78 3.15c-.06.15-.21.37-.55.29.01.02-.87-.22-.87-.22l-.6 1.37 1.56.39c.29.07.58.15.86.22l-.5 2.01 1.2.3.49-1.98c.33.09.65.17.96.25l-.49 1.96 1.2.3.5-1.99c2.04.39 3.58.23 4.23-1.62.52-1.49-.03-2.35-1.1-2.91.78-.18 1.37-.69 1.53-1.74Zm-2.74 3.84c-.37 1.49-2.89.68-3.71.48l.66-2.65c.82.2 3.44.61 3.05 2.17Zm.37-3.86c-.34 1.36-2.44.67-3.12.5l.6-2.4c.68.17 2.87.49 2.52 1.9Z"
            fill="#FFFFFF"
          />
        </svg>
      );

    case 'eth':
      return (
        <svg
          width={size}
          height={size}
          viewBox="0 0 24 24"
          fill="none"
          aria-hidden="true"
          className={baseClass}
        >
          <circle cx="12" cy="12" r="11" fill="#627EEA" />
          <path d="M12 4.2 7.2 12.15 12 9.95V4.2Z" fill="#FFFFFF" fillOpacity="0.85" />
          <path d="M12 4.2v5.75l4.8 2.2L12 4.2Z" fill="#FFFFFF" />
          <path d="M12 15.05 7.2 12.2 12 9.95v5.1Z" fill="#FFFFFF" fillOpacity="0.65" />
          <path d="M12 15.05v-5.1l4.8 2.25-4.8 2.85Z" fill="#FFFFFF" fillOpacity="0.9" />
          <path d="M12 19.8 7.2 13.1 12 15.95V19.8Z" fill="#FFFFFF" fillOpacity="0.85" />
          <path d="M12 19.8v-3.85l4.8-2.85L12 19.8Z" fill="#FFFFFF" />
        </svg>
      );

    case 'arb':
      return (
        <svg
          width={size}
          height={size}
          viewBox="0 0 24 24"
          fill="none"
          aria-hidden="true"
          className={baseClass}
        >
          <circle cx="12" cy="12" r="11" fill="#213147" />
          <path
            d="M12 3.3 5 7.3v9.4l7 4 7-4V7.3l-7-4Z"
            stroke="#28A0F0"
            strokeWidth="1.5"
            fill="#1B283B"
          />
          <path
            d="M11.15 7.2 7.2 15.7l1.65.95 3.95-8.5-1.65-.95ZM13.85 7.2l-3.2 6.95 1.55 1.15 3.2-6.95-1.55-1.15Z"
            fill="#FFFFFF"
          />
          <path
            d="M14.1 10.6 16.8 16l-1.6.95-1.95-3.95.85-2.4Z"
            fill="#28A0F0"
          />
        </svg>
      );

    case 'op':
      return (
        <svg
          width={size}
          height={size}
          viewBox="0 0 24 24"
          fill="none"
          aria-hidden="true"
          className={baseClass}
        >
          <circle cx="12" cy="12" r="11" fill="#FF0420" />
          <path
            d="M8.6 15.4c-1.85 0-3.1-1.25-3.1-3.15 0-2.1 1.45-3.65 3.45-3.65 1.85 0 3.1 1.25 3.1 3.15 0 2.1-1.45 3.65-3.45 3.65Zm.25-5.15c-.95 0-1.65.85-1.65 1.95 0 .95.55 1.55 1.4 1.55.95 0 1.65-.85 1.65-1.95 0-.95-.55-1.55-1.4-1.55Zm4.1 5.05 1.15-6.6h2.65c1.55 0 2.55.8 2.55 2.1 0 1.55-1.15 2.5-2.85 2.5h-1.35l-.35 2h-1.8Zm2.45-3.5h1.05c.65 0 1.1-.4 1.1-1 0-.45-.35-.75-.95-.75h-.9l-.3 1.75Z"
            fill="#FFFFFF"
          />
        </svg>
      );

    case 'base':
      return (
        <svg
          width={size}
          height={size}
          viewBox="0 0 24 24"
          fill="none"
          aria-hidden="true"
          className={baseClass}
        >
          <circle cx="12" cy="12" r="11" fill="#0052FF" />
          <path
            d="M12.15 18.5a6.5 6.5 0 1 0 0-13 6.5 6.5 0 0 0-6.43 5.6h8.93v1.8H5.72a6.5 6.5 0 0 0 6.43 5.6Z"
            fill="#FFFFFF"
          />
        </svg>
      );

    case 'poly':
      return (
        <svg
          width={size}
          height={size}
          viewBox="0 0 24 24"
          fill="none"
          aria-hidden="true"
          className={baseClass}
        >
          <circle cx="12" cy="12" r="11" fill="#8247E5" />
          <path
            d="M15.4 9.2c-.35-.2-.8-.2-1.15 0l-2.1 1.2-1.45.82-2.05 1.2c-.35.2-.8.2-1.15 0l-1.55-.9c-.35-.2-.55-.58-.55-.98V8.7c0-.4.2-.78.55-.98l1.55-.9c.35-.2.8-.2 1.15 0l1.55.9c.35.2.55.58.55.98v1.15l1.45-.84V7.85c0-.4-.2-.78-.55-.98L8.65 5.1c-.35-.2-.8-.2-1.15 0L4.45 6.87c-.35.2-.55.58-.55.98v3.52c0 .4.2.78.55.98l3.05 1.76c.35.2.8.2 1.15 0l2.05-1.18 1.45-.84 2.05-1.18c.35-.2.8-.2 1.15 0l1.55.9c.35.2.55.58.55.98v1.8c0 .4-.2.78-.55.98l-1.55.9c-.35.2-.8.2-1.15 0l-1.55-.9c-.35-.2-.55-.58-.55-.98v-1.15l-1.45.84v1.16c0 .4.2.78.55.98l3.05 1.76c.35.2.8.2 1.15 0l3.05-1.76c.35-.2.55-.58.55-.98v-3.52c0-.4-.2-.78-.55-.98L15.4 9.2Z"
            fill="#FFFFFF"
          />
        </svg>
      );

    case 'tron':
      return (
        <svg
          width={size}
          height={size}
          viewBox="0 0 24 24"
          fill="none"
          aria-hidden="true"
          className={baseClass}
        >
          <circle cx="12" cy="12" r="11" fill="#EF0027" />
          <path
            d="M6.2 6.3 17.5 8.4l1.4 2.8-7.4 7.7L6.2 6.3Zm1.9 1.6 3.2 8.2 1.1-5.1-4.3-3.1Zm5.4 3.5-1 4.6 4.7-4.9-3.7.3Zm-3.6-2.9 3.6 2.5 2.7-.2-.8-1.5-5.5-.8Z"
            fill="#FFFFFF"
          />
        </svg>
      );

    case 'sol': {
      const gradId = `sol-grad-${uid}`;
      return (
        <svg
          width={size}
          height={size}
          viewBox="0 0 24 24"
          fill="none"
          aria-hidden="true"
          className={baseClass}
        >
          <defs>
            <linearGradient id={gradId} x1="5" y1="18" x2="19" y2="6" gradientUnits="userSpaceOnUse">
              <stop offset="0%" stopColor="#9945FF" />
              <stop offset="50%" stopColor="#19FB9B" />
              <stop offset="100%" stopColor="#00FFA3" />
            </linearGradient>
          </defs>
          <circle cx="12" cy="12" r="11" fill="#14151A" stroke="#27272A" strokeWidth="1" />
          <path
            d="M7.6 15.2c.15-.15.36-.24.58-.24h9.52c.36 0 .54.44.28.7l-1.6 1.6c-.15.15-.36.24-.58.24H6.28c-.36 0-.54-.44-.28-.7l1.6-1.6Zm0-8.7c.15-.15.36-.24.58-.24h9.52c.36 0 .54.44.28.7l-1.6 1.6c-.15.15-.36.24-.58.24H6.28c-.36 0-.54-.44-.28-.7l1.6-1.6Zm8.8 4.25c-.15-.15-.36-.24-.58-.24H6.3c-.36 0-.54.44-.28.7l1.6 1.6c.15.15.36.24.58.24h9.52c.36 0 .54-.44.28-.7l-1.6-1.6Z"
            fill={`url(#${gradId})`}
          />
        </svg>
      );
    }

    case 'usdt':
      return (
        <svg
          width={size}
          height={size}
          viewBox="0 0 24 24"
          fill="none"
          aria-hidden="true"
          className={baseClass}
        >
          <circle cx="12" cy="12" r="11" fill="#26A17B" />
          <path
            d="M13.25 10.65V9.1h3.55V6.7H7.2v2.4h3.55v1.55c-3.05.14-5.35.75-5.35 1.48 0 .73 2.3 1.34 5.35 1.48v5.29h2.5v-5.29c3.05-.14 5.35-.75 5.35-1.48 0-.73-2.3-1.34-5.35-1.48Zm0 2.52v-.02c-.4.03-.82.04-1.25.04-.43 0-.85-.01-1.25-.04v.02c-2.55-.11-4.45-.55-4.45-1.08 0-.53 1.9-.97 4.45-1.08v1.72c.4.03.82.05 1.25.05.43 0 .85-.02 1.25-.05V11c2.55.11 4.45.55 4.45 1.08 0 .53-1.9.97-4.45 1.08Z"
            fill="#FFFFFF"
          />
        </svg>
      );

    case 'usdc':
      return (
        <svg
          width={size}
          height={size}
          viewBox="0 0 24 24"
          fill="none"
          aria-hidden="true"
          className={baseClass}
        >
          <circle cx="12" cy="12" r="11" fill="#2775CA" />
          <path
            d="M9.1 5.6a7.2 7.2 0 0 0 0 12.8l.75-1.38a5.65 5.65 0 0 1 0-10.04L9.1 5.6Zm5.8 0-.75 1.38a5.65 5.65 0 0 1 0 10.04l.75 1.38a7.2 7.2 0 0 0 0-12.8Z"
            fill="#FFFFFF"
          />
          <path
            d="M12.7 11.2c-1.15-.3-1.55-.52-1.55-1.02 0-.52.48-.86 1.22-.86.72 0 1.15.3 1.25.85h1.38c-.1-1.02-.78-1.78-1.92-1.98V6.9h-1.42v1.3c-1.18.22-1.95.98-1.95 2.04 0 1.22.92 1.78 2.38 2.12 1.18.28 1.52.58 1.52 1.12 0 .58-.52.96-1.35.96-.86 0-1.38-.38-1.48-1.02H9.38c.12 1.18.92 1.92 2.28 2.14v1.34h1.42v-1.32c1.25-.2 2.08-.98 2.08-2.16 0-1.26-.88-1.84-2.46-2.22Z"
            fill="#FFFFFF"
          />
        </svg>
      );

    default: {
      const ch = (label.trim()[0] || '?').toUpperCase();
      return (
        <svg
          width={size}
          height={size}
          viewBox="0 0 24 24"
          fill="none"
          aria-hidden="true"
          className={baseClass}
        >
          <circle cx="12" cy="12" r="11" fill="#27272A" stroke="#52525B" strokeWidth="1.2" />
          <text
            x="12"
            y="16"
            textAnchor="middle"
            fill="#E4E4E7"
            fontSize="11"
            fontWeight="700"
            fontFamily="ui-monospace, SFMono-Regular, Menlo, monospace"
          >
            {ch}
          </text>
        </svg>
      );
    }
  }
}
