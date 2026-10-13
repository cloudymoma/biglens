import { useEffect, useId, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { CircleHelp } from 'lucide-react';

// A "?" button whose tooltip opens on mouse hover, keyboard focus, or
// click/tap, stays open while hovered over the trigger or bubble, closes on
// Escape from anywhere on the page (WCAG 1.4.13), and is announced via
// aria-describedby. Content is plain JSX — never HTML strings.
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
  const rootRef = useRef<HTMLSpanElement>(null);
  const [hovered, setHovered] = useState(false);
  const [focused, setFocused] = useState(false);
  const [pinned, setPinned] = useState(false);
  const open = hovered || focused || pinned;

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setHovered(false);
        setFocused(false);
        setPinned(false);
      }
    };
    const onPointerDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
        setPinned(false);
      }
    };
    document.addEventListener('keydown', onKeyDown);
    document.addEventListener('mousedown', onPointerDown);
    return () => {
      document.removeEventListener('keydown', onKeyDown);
      document.removeEventListener('mousedown', onPointerDown);
    };
  }, [open]);

  return (
    <span
      ref={rootRef}
      className="relative inline-flex items-center"
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
    >
      <button
        type="button"
        aria-label={label}
        aria-describedby={id}
        onFocus={() => setFocused(true)}
        onBlur={() => setFocused(false)}
        onClick={() => setPinned(p => !p)}
        className="inline-flex items-center rounded-full text-zinc-500 hover:text-zinc-200 focus:outline-none focus-visible:ring-2 focus-visible:ring-zinc-500"
      >
        <CircleHelp size={14} aria-hidden="true" />
      </button>
      <span
        id={id}
        role="tooltip"
        className={`absolute left-1/2 top-full z-20 pt-2 -translate-x-1/2 ${
          open ? 'visible opacity-100' : 'invisible opacity-0 pointer-events-none'
        } transition-opacity`}
      >
        <span
          className={`block ${width} max-w-[80vw] rounded-lg border border-zinc-700 bg-zinc-900 px-3 py-2 text-[11px] leading-relaxed font-normal text-zinc-300 shadow-xl`}
        >
          {children}
        </span>
      </span>
    </span>
  );
}
