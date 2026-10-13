import { useId, useState } from 'react';
import type { ReactNode } from 'react';
import { CircleHelp } from 'lucide-react';

// A "?" button whose tooltip opens on mouse hover and on keyboard focus (and
// on tap), closes on Escape, and is announced via aria-describedby. Content is
// plain JSX — never HTML strings.
export default function HelpTip({
  label,
  children,
  width = 'w-80',
}: {
  label: string; // accessible name of the button, e.g. "What does the BigQuery profile add?"
  children: ReactNode;
  width?: string; // Tailwind width class for the bubble
}) {
  const id = useId();
  const [open, setOpen] = useState(false);
  return (
    <span
      className="relative inline-flex items-center"
      onMouseEnter={() => setOpen(true)}
      onMouseLeave={() => setOpen(false)}
    >
      <button
        type="button"
        aria-label={label}
        aria-describedby={id}
        aria-expanded={open}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onClick={() => setOpen(o => !o)}
        onKeyDown={e => {
          if (e.key === 'Escape') setOpen(false);
        }}
        className="inline-flex items-center rounded-full text-zinc-500 hover:text-zinc-200 focus:outline-none focus-visible:ring-2 focus-visible:ring-zinc-500"
      >
        <CircleHelp size={14} aria-hidden="true" />
      </button>
      <span
        id={id}
        role="tooltip"
        className={`absolute left-1/2 top-full z-20 mt-2 -translate-x-1/2 ${width} max-w-[80vw] rounded-lg border border-zinc-700 bg-zinc-900 px-3 py-2 text-[11px] leading-relaxed font-normal text-zinc-300 shadow-xl transition-opacity ${
          open ? 'visible opacity-100' : 'invisible opacity-0 pointer-events-none'
        }`}
      >
        {children}
      </span>
    </span>
  );
}
