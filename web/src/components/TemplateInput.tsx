import { useId, useLayoutEffect, useRef, useState } from "react";
import { NAMING_TOKENS } from "./namingTokens";

/**
 * A template field with a palette of clickable tokens.
 *
 * Tokens go in at the caret (replacing any selection) rather than at the end,
 * so a token can be dropped into the middle of a template that is already
 * written. The field stays a normal text input — typing `{author}` by hand
 * still works, the palette is only a shortcut.
 */
export function TemplateInput({
  label,
  value,
  onChange,
  placeholder,
  includeTrackTokens,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  /** Track tokens render empty for a single-file book, so only offer them where they mean something. */
  includeTrackTokens?: boolean;
}) {
  const id = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  // Where to put the caret once React has re-rendered with the new value.
  const pendingCaret = useRef<number | null>(null);
  const [open, setOpen] = useState(false);

  useLayoutEffect(() => {
    const caret = pendingCaret.current;
    if (caret === null || !inputRef.current) return;
    pendingCaret.current = null;
    inputRef.current.focus();
    inputRef.current.setSelectionRange(caret, caret);
  });

  /** Replace the current selection (or insert at the caret) and say where the caret lands. */
  function splice(replace: (selected: string) => { text: string; caret: number }) {
    const el = inputRef.current;
    const start = el?.selectionStart ?? value.length;
    const end = el?.selectionEnd ?? value.length;
    const { text, caret } = replace(value.slice(start, end));
    pendingCaret.current = start + caret;
    onChange(value.slice(0, start) + text + value.slice(end));
  }

  const tokens = NAMING_TOKENS.filter((t) => includeTrackTokens || !t.trackOnly);

  return (
    <div className="text-sm">
      <label htmlFor={id} className="mb-1 block text-slate-500">
        {label}
      </label>
      <input
        id={id}
        ref={inputRef}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="w-full rounded border border-slate-300 px-2 py-1.5 font-mono text-xs dark:border-slate-700 dark:bg-slate-800"
      />

      <div className="mt-1.5 flex flex-wrap items-center gap-1">
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen((v) => !v)}
          className="rounded px-1.5 py-0.5 text-xs text-slate-500 hover:text-slate-700 dark:hover:text-slate-300"
        >
          {open ? "Hide tokens" : "Insert token"}
        </button>
        {open &&
          tokens.map((t) => (
            <button
              key={t.token}
              type="button"
              title={`${t.meaning} — e.g. ${t.example}`}
              // Keep the caret: a plain click would blur the input first.
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => splice(() => ({ text: t.token, caret: t.token.length }))}
              className="rounded border border-slate-200 bg-slate-50 px-1.5 py-0.5 font-mono text-[11px] text-slate-600 hover:bg-slate-100 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700"
            >
              {t.token}
            </button>
          ))}
        {open && (
          <button
            type="button"
            title="Optional group: drops out entirely when a token inside it is empty"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() =>
              splice((selected) => ({ text: `[${selected}]`, caret: selected.length + 2 }))
            }
            className="rounded border border-slate-200 bg-slate-50 px-1.5 py-0.5 font-mono text-[11px] text-slate-600 hover:bg-slate-100 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700"
          >
            [ … ]
          </button>
        )}
      </div>
    </div>
  );
}
