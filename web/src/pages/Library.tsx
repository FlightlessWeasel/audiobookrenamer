import { useState } from "react";
import { client, type Library } from "../api/client";
import { RootPathField } from "../components/FolderPicker";
import { NamingGuide } from "../components/NamingGuide";
import { TemplateInput } from "../components/TemplateInput";
import { useAction } from "../lib/useAction";
import { useAsync } from "../lib/useAsync";

type LibAction = "scan" | "match" | "remove";

// Mirrors internal/model.DefaultFileTemplate / DefaultMultiFileTemplate. Shown
// as input placeholders; leaving a template field blank resets it to these.
const DEFAULT_FILE_TEMPLATE = "{title}[ ({year})] - {author}{ext}";
const DEFAULT_MULTI_FILE_TEMPLATE = "{title} ({year}) - {track2}{ext}";

export function LibraryPage() {
  const libs = useAsync((signal) => client.listLibraries({ signal }), []);
  // One action runs at a time across all libraries (`busy`); `isBusy(id)` tells
  // which library's buttons to disable, and `action` which verb it is so the
  // right button shows its pending label.
  const { run: perform, busy, isBusy, error: err, mounted } = useAction();
  const [action, setAction] = useState<LibAction>("scan");
  const [editing, setEditing] = useState<string | null>(null);

  function run(id: string, kind: LibAction, fn: () => Promise<unknown>) {
    if (busy) return;
    setAction(kind);
    perform(async () => {
      await fn();
      if (kind === "remove" && mounted.current) libs.reload();
    }, id);
  }

  const scan = (id: string) => run(id, "scan", () => client.scanLibrary(id));
  const match = (id: string) => run(id, "match", () => client.matchLibrary(id));
  const remove = (id: string) => {
    if (!confirm("Remove this library? Scanned book records are deleted; files on disk are untouched.")) return;
    void run(id, "remove", () => client.deleteLibrary(id));
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Libraries</h1>
      </div>

      <AddLibraryForm onAdded={libs.reload} />

      {err && <p className="rounded bg-red-100 px-3 py-2 text-sm text-red-800">{err}</p>}
      {libs.loading && <p className="text-sm text-slate-500">Loading…</p>}
      {libs.error && <p className="text-sm text-red-600">{libs.error}</p>}

      <div className="space-y-3">
        {libs.data?.map((l) => (
          <div
            key={l.id}
            className="rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900"
          >
            <div className="flex items-center justify-between">
              <div className="min-w-0">
                <div className="font-medium">{l.name}</div>
                <div className="truncate text-sm text-slate-500">{l.root_path}</div>
                <div className="mt-1 text-xs text-slate-400">
                  {l.structure_mode === "series_first" ? "Series → Author → Book" : "Author → Series → Book"}
                  {" · "}
                  {l.author_folder_mode === "name" ? "author name" : "author sort name"}
                  {" · "}
                  {l.enabled ? "enabled" : "disabled"}
                </div>
              </div>
              <div className="flex shrink-0 gap-2">
                <button
                  onClick={() => setEditing(editing === l.id ? null : l.id)}
                  className="rounded border border-slate-300 px-3 py-1.5 text-sm disabled:opacity-50 dark:border-slate-700"
                >
                  {editing === l.id ? "Close" : "Edit"}
                </button>
                <button
                  onClick={() => scan(l.id)}
                  disabled={isBusy(l.id)}
                  className="rounded bg-slate-900 px-3 py-1.5 text-sm font-medium text-white disabled:opacity-50 dark:bg-slate-100 dark:text-slate-900"
                >
                  {isBusy(l.id) && action === "scan" ? "Queuing…" : "Scan"}
                </button>
                <button
                  onClick={() => match(l.id)}
                  disabled={isBusy(l.id)}
                  className="rounded border border-slate-300 px-3 py-1.5 text-sm disabled:opacity-50 dark:border-slate-700"
                >
                  {isBusy(l.id) && action === "match" ? "Queuing…" : "Match"}
                </button>
                <button
                  onClick={() => remove(l.id)}
                  disabled={isBusy(l.id)}
                  className="rounded border border-slate-300 px-3 py-1.5 text-sm disabled:opacity-50 dark:border-slate-700"
                >
                  {isBusy(l.id) && action === "remove" ? "Removing…" : "Remove"}
                </button>
              </div>
            </div>

            {editing === l.id && (
              <EditLibraryForm
                library={l}
                onSaved={() => {
                  setEditing(null);
                  libs.reload();
                }}
              />
            )}
          </div>
        ))}
        {libs.data?.length === 0 && (
          <p className="text-sm text-slate-500">No libraries yet. Add one above.</p>
        )}
      </div>
    </div>
  );
}

function TemplateFields({
  fileTemplate,
  multiFileTemplate,
  onFile,
  onMulti,
}: {
  fileTemplate: string;
  multiFileTemplate: string;
  onFile: (v: string) => void;
  onMulti: (v: string) => void;
}) {
  return (
    <>
      <TemplateInput
        label="Single-file template"
        value={fileTemplate}
        onChange={onFile}
        placeholder={DEFAULT_FILE_TEMPLATE}
      />
      <TemplateInput
        label="Multi-file (per-track) template"
        value={multiFileTemplate}
        onChange={onMulti}
        placeholder={DEFAULT_MULTI_FILE_TEMPLATE}
        includeTrackTokens
      />
      <p className="text-xs text-slate-400">Leave blank to reset to the default.</p>
      <NamingGuide />
    </>
  );
}

function EditLibraryForm({ library, onSaved }: { library: Library; onSaved: () => void }) {
  const [name, setName] = useState(library.name);
  const [rootPath, setRootPath] = useState(library.root_path);
  const [structure, setStructure] = useState<Library["structure_mode"]>(library.structure_mode);
  const [authorFolder, setAuthorFolder] = useState<Library["author_folder_mode"]>(
    library.author_folder_mode,
  );
  const [fileTemplate, setFileTemplate] = useState(library.file_template);
  const [multiFileTemplate, setMultiFileTemplate] = useState(library.multi_file_template);
  const [enabled, setEnabled] = useState(library.enabled);
  const { run, busy: saving, error: err, mounted } = useAction();

  function submit(e: React.FormEvent) {
    e.preventDefault();
    run(async () => {
      await client.updateLibrary(library.id, {
        name,
        root_path: rootPath,
        structure_mode: structure,
        author_folder_mode: authorFolder,
        file_template: fileTemplate,
        multi_file_template: multiFileTemplate,
        enabled,
      });
      if (mounted.current) onSaved();
    });
  }

  return (
    <form onSubmit={submit} className="mt-4 space-y-3 border-t border-slate-200 pt-4 dark:border-slate-800">
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="text-sm">
          <span className="mb-1 block text-slate-500">Name</span>
          <input
            required
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="w-full rounded border border-slate-300 px-2 py-1.5 dark:border-slate-700 dark:bg-slate-800"
          />
        </label>
        <label className="text-sm">
          <span className="mb-1 block text-slate-500">Structure</span>
          <select
            value={structure}
            onChange={(e) => setStructure(e.target.value as Library["structure_mode"])}
            className="w-full rounded border border-slate-300 px-2 py-1.5 dark:border-slate-700 dark:bg-slate-800"
          >
            <option value="author_first">Author → Series → Book (default)</option>
            <option value="series_first">Series → Author → Book</option>
          </select>
        </label>
        <label className="text-sm">
          <span className="mb-1 block text-slate-500">Author folder</span>
          <select
            value={authorFolder}
            onChange={(e) => setAuthorFolder(e.target.value as Library["author_folder_mode"])}
            className="w-full rounded border border-slate-300 px-2 py-1.5 dark:border-slate-700 dark:bg-slate-800"
          >
            <option value="sort">Sort name — Campbell, Jack (default)</option>
            <option value="name">Author name — Jack Campbell</option>
          </select>
        </label>
      </div>
      <RootPathField value={rootPath} onChange={setRootPath} />
      <TemplateFields
        fileTemplate={fileTemplate}
        multiFileTemplate={multiFileTemplate}
        onFile={setFileTemplate}
        onMulti={setMultiFileTemplate}
      />
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        Enabled
      </label>
      {err && <p className="text-sm text-red-600">{err}</p>}
      <div className="flex gap-2">
        <button
          type="submit"
          disabled={saving}
          className="rounded bg-slate-900 px-3 py-1.5 text-sm font-medium text-white disabled:opacity-50 dark:bg-slate-100 dark:text-slate-900"
        >
          {saving ? "Saving…" : "Save changes"}
        </button>
      </div>
    </form>
  );
}

function AddLibraryForm({ onAdded }: { onAdded: () => void }) {
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState<Partial<Library>>({
    structure_mode: "author_first",
    author_folder_mode: "sort",
    enabled: true,
  });
  const { run, busy: saving, error: err, mounted } = useAction();

  function submit(e: React.FormEvent) {
    e.preventDefault();
    run(async () => {
      await client.createLibrary(form);
      if (mounted.current) {
        setForm({
          structure_mode: "author_first",
          author_folder_mode: "sort",
          enabled: true,
        });
        setOpen(false);
      }
      onAdded();
    });
  }

  if (!open) {
    return (
      <button
        onClick={() => setOpen(true)}
        className="rounded border border-dashed border-slate-300 px-4 py-2 text-sm text-slate-600 dark:border-slate-700 dark:text-slate-300"
      >
        + Add library
      </button>
    );
  }

  return (
    <form
      onSubmit={submit}
      className="space-y-3 rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900"
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="text-sm">
          <span className="mb-1 block text-slate-500">Name</span>
          <input
            required
            value={form.name ?? ""}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
            className="w-full rounded border border-slate-300 px-2 py-1.5 dark:border-slate-700 dark:bg-slate-800"
          />
        </label>
        <label className="text-sm">
          <span className="mb-1 block text-slate-500">Structure</span>
          <select
            value={form.structure_mode}
            onChange={(e) => setForm({ ...form, structure_mode: e.target.value as Library["structure_mode"] })}
            className="w-full rounded border border-slate-300 px-2 py-1.5 dark:border-slate-700 dark:bg-slate-800"
          >
            <option value="author_first">Author → Series → Book (default)</option>
            <option value="series_first">Series → Author → Book</option>
          </select>
        </label>
        <label className="text-sm">
          <span className="mb-1 block text-slate-500">Author folder</span>
          <select
            value={form.author_folder_mode}
            onChange={(e) => setForm({ ...form, author_folder_mode: e.target.value as Library["author_folder_mode"] })}
            className="w-full rounded border border-slate-300 px-2 py-1.5 dark:border-slate-700 dark:bg-slate-800"
          >
            <option value="sort">Sort name — Campbell, Jack (default)</option>
            <option value="name">Author name — Jack Campbell</option>
          </select>
        </label>
      </div>
      <RootPathField
        value={form.root_path ?? ""}
        onChange={(v) => setForm({ ...form, root_path: v })}
      />
      <TemplateFields
        fileTemplate={form.file_template ?? ""}
        multiFileTemplate={form.multi_file_template ?? ""}
        onFile={(v) => setForm({ ...form, file_template: v })}
        onMulti={(v) => setForm({ ...form, multi_file_template: v })}
      />
      {err && <p className="text-sm text-red-600">{err}</p>}
      <div className="flex gap-2">
        <button
          type="submit"
          disabled={saving}
          className="rounded bg-slate-900 px-3 py-1.5 text-sm font-medium text-white disabled:opacity-50 dark:bg-slate-100 dark:text-slate-900"
        >
          {saving ? "Saving…" : "Save"}
        </button>
        <button
          type="button"
          onClick={() => setOpen(false)}
          className="rounded border border-slate-300 px-3 py-1.5 text-sm dark:border-slate-700"
        >
          Cancel
        </button>
      </div>
    </form>
  );
}
