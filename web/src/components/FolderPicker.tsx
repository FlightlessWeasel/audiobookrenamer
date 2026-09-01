import { useEffect, useState } from "react";
import { client } from "../api/client";
import { useAsync } from "../lib/useAsync";

/**
 * Modal folder browser for choosing a library root.
 *
 * It walks the *server's* filesystem over /api/browse rather than using a file
 * input: a browser never hands back a path the server could open, and the
 * server is often on another machine or in a container where the library sits
 * at a path (`/audiobooks`) that does not exist on the viewer's own computer.
 */
export function FolderPicker({
  initialPath,
  onPick,
  onClose,
}: {
  initialPath?: string;
  onPick: (path: string) => void;
  onClose: () => void;
}) {
  // "" is the root listing (drives on Windows, "/" elsewhere). Start from the
  // folder already in the field so re-opening the picker resumes where the
  // path points.
  const [path, setPath] = useState(initialPath?.trim() ?? "");
  const listing = useAsync((signal) => client.browse(path, { signal }), [path]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const current = listing.data;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label="Choose a folder"
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
      onClick={onClose}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="flex max-h-[80vh] w-full max-w-xl flex-col rounded-lg border border-slate-200 bg-white shadow-lg dark:border-slate-800 dark:bg-slate-900"
      >
        <div className="border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <h2 className="text-sm font-medium">Choose a folder</h2>
          <p className="mt-1 truncate font-mono text-xs text-slate-500">
            {path || "This computer"}
          </p>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto p-2">
          {listing.loading && (
            <p className="px-2 py-1 text-sm text-slate-500">Loading…</p>
          )}
          {listing.error && (
            <p role="alert" className="px-2 py-1 text-sm text-red-600">
              {listing.error}
            </p>
          )}

          {current && (
            <ul className="space-y-0.5">
              {path !== "" && (
                <li>
                  <button
                    type="button"
                    onClick={() => setPath(current.parent)}
                    className="w-full rounded px-2 py-1.5 text-left text-sm hover:bg-slate-100 dark:hover:bg-slate-800"
                  >
                    ← Up
                  </button>
                </li>
              )}
              {current.entries.map((e) => (
                <li key={e.path}>
                  <button
                    type="button"
                    onClick={() => setPath(e.path)}
                    className="w-full truncate rounded px-2 py-1.5 text-left text-sm hover:bg-slate-100 dark:hover:bg-slate-800"
                  >
                    📁 {e.name}
                  </button>
                </li>
              ))}
              {current.entries.length === 0 && (
                <li className="px-2 py-1 text-sm text-slate-500">
                  No sub-folders here. You can still select this folder.
                </li>
              )}
              {current.truncated && (
                <li className="px-2 py-1 text-xs text-slate-400">
                  Only the first {current.entries.length} folders are shown.
                </li>
              )}
            </ul>
          )}
        </div>

        <div className="flex items-center justify-end gap-2 border-t border-slate-200 px-4 py-3 dark:border-slate-800">
          <button
            type="button"
            onClick={onClose}
            className="rounded border border-slate-300 px-3 py-1.5 text-sm dark:border-slate-700"
          >
            Cancel
          </button>
          <button
            type="button"
            disabled={!path}
            onClick={() => {
              onPick(path);
              onClose();
            }}
            className="rounded bg-slate-900 px-3 py-1.5 text-sm font-medium text-white disabled:opacity-50 dark:bg-slate-100 dark:text-slate-900"
          >
            Use this folder
          </button>
        </div>
      </div>
    </div>
  );
}

/**
 * The root-path field: a text input (still typeable, which is the only way to
 * reach a hidden folder) plus a Browse button that opens the picker.
 */
export function RootPathField({
  value,
  onChange,
}: {
  value: string;
  onChange: (v: string) => void;
}) {
  const [picking, setPicking] = useState(false);
  return (
    <div className="block text-sm">
      <span className="mb-1 block text-slate-500">Root path (absolute)</span>
      <div className="flex gap-2">
        <input
          required
          aria-label="Root path (absolute)"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder="C:\Audiobooks or /mnt/audiobooks"
          className="w-full rounded border border-slate-300 px-2 py-1.5 font-mono text-xs dark:border-slate-700 dark:bg-slate-800"
        />
        <button
          type="button"
          onClick={() => setPicking(true)}
          className="shrink-0 rounded border border-slate-300 px-3 py-1.5 text-sm dark:border-slate-700"
        >
          Browse…
        </button>
      </div>
      {picking && (
        <FolderPicker
          initialPath={value}
          onPick={onChange}
          onClose={() => setPicking(false)}
        />
      )}
    </div>
  );
}
