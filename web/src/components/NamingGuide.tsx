/**
 * Reference for the library naming settings, shown inline next to the template
 * fields. The facts here mirror internal/organize (template.go, plan.go,
 * sanitize.go) — keep them in step when the renderer changes.
 */

import { NAMING_TOKENS } from "./namingTokens";

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="space-y-1.5">
      <h4 className="text-xs font-semibold uppercase tracking-wide text-slate-500">{title}</h4>
      {children}
    </section>
  );
}

function Code({ children }: { children: React.ReactNode }) {
  return (
    <code className="rounded bg-slate-100 px-1 py-0.5 font-mono text-[11px] dark:bg-slate-800">
      {children}
    </code>
  );
}

export function NamingGuide() {
  return (
    <details className="rounded border border-slate-200 dark:border-slate-800">
      <summary className="cursor-pointer px-3 py-2 text-sm text-slate-600 dark:text-slate-300">
        File naming guide
      </summary>

      <div className="space-y-4 border-t border-slate-200 px-3 py-3 text-sm dark:border-slate-800">
        <Section title="What each setting controls">
          <p className="text-slate-600 dark:text-slate-400">
            <strong>Structure</strong> decides the folders a book is filed under.{" "}
            <strong>Single-file template</strong> names the file of a book that is one
            audio file. <strong>Multi-file template</strong> names each track of a book
            split across several files. Renames happen in place, and you always see a
            dry-run diff on the Organize page before anything moves.
          </p>
        </Section>

        <Section title="Folder structure">
          <ul className="space-y-1 text-slate-600 dark:text-slate-400">
            <li>
              <strong>Author → Series → Book</strong> (default):{" "}
              <Code>Sanderson, Brandon/Mistborn/The Final Empire (2006)/</Code>
            </li>
            <li>
              <strong>Series → Author → Book</strong>: <Code>Mistborn/Sanderson, Brandon/The Final Empire (2006)/</Code>{" "}
              — better for shared-world series and multi-author anthologies.
            </li>
          </ul>
          <p className="text-slate-500">
            The series folder is dropped for a standalone book. The author folder uses the
            sort name (<Code>King, Stephen</Code>) when one is known — edit it per book on
            the book detail page. The book folder itself is always{" "}
            <Code>{"{title}[ ({year})]"}</Code> and is not configurable.
          </p>
        </Section>

        <Section title="Tokens">
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-left text-xs">
              <thead>
                <tr className="border-b border-slate-200 text-slate-500 dark:border-slate-800">
                  <th className="py-1 pr-3 font-medium">Token</th>
                  <th className="py-1 pr-3 font-medium">Meaning</th>
                  <th className="py-1 font-medium">Example</th>
                </tr>
              </thead>
              <tbody>
                {NAMING_TOKENS.map((t) => (
                  <tr key={t.token} className="border-b border-slate-100 dark:border-slate-800/60">
                    <td className="py-1 pr-3 align-top">
                      <Code>{t.token}</Code>
                    </td>
                    <td className="py-1 pr-3 align-top text-slate-600 dark:text-slate-400">
                      {t.meaning}
                    </td>
                    <td className="py-1 align-top font-mono text-slate-500">{t.example}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="text-slate-500">
            Track tokens are empty for a single-file book. When a book&apos;s files carry
            distinct track numbers those are used; otherwise files are numbered by their
            sorted order.
          </p>
        </Section>

        <Section title="Optional groups">
          <p className="text-slate-600 dark:text-slate-400">
            Wrap part of a template in <Code>[ … ]</Code> and the whole group disappears if
            any token inside it is empty. That is how a missing year avoids leaving stray
            punctuation behind:
          </p>
          <ul className="space-y-1 font-mono text-xs text-slate-500">
            <li>
              <Code>{"{title}[ ({year})] - {author}{ext}"}</Code>
            </li>
            <li>year 2006 → <span className="text-slate-600 dark:text-slate-400">The Final Empire (2006) - Brandon Sanderson.m4b</span></li>
            <li>year unknown → <span className="text-slate-600 dark:text-slate-400">The Final Empire - Brandon Sanderson.m4b</span></li>
          </ul>
        </Section>

        <Section title="Defaults">
          <ul className="space-y-1 text-slate-600 dark:text-slate-400">
            <li>
              Single-file: <Code>{"{title}[ ({year})] - {author}{ext}"}</Code>
            </li>
            <li>
              Multi-file: <Code>{"{title} ({year}) - {track2}{ext}"}</Code>
            </li>
          </ul>
          <p className="text-slate-500">Clear a template field to go back to its default.</p>
        </Section>

        <Section title="Good to know">
          <ul className="list-disc space-y-1 pl-4 text-slate-600 dark:text-slate-400">
            <li>
              The extension is appended automatically if your template does not end with{" "}
              <Code>{"{ext}"}</Code>, so files never lose it.
            </li>
            <li>
              Characters no filesystem accepts (<Code>{'< > : " / \\ | ? *'}</Code>) become
              spaces, runs of spaces collapse, and trailing dots and spaces are trimmed.
              Windows device names like <Code>CON</Code> get an underscore prefix.
            </li>
            <li>
              A token that resolves to nothing outside an optional group leaves a gap; a
              segment that ends up completely empty becomes <Code>Unknown</Code>.
            </li>
            <li>
              Targets are kept inside the platform path limit (259 characters on Windows,
              1024 elsewhere). Folder names are shortened to fit; file names never are, so
              track numbers stay intact. A book that still cannot fit is skipped with that
              reason shown in the Organize preview.
            </li>
            <li>
              A typo like <Code>{"{tilte}"}</Code> is rejected when you save, so it can
              never be baked into every filename.
            </li>
          </ul>
        </Section>
      </div>
    </details>
  );
}
