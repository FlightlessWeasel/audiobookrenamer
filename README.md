# Audiobook Library Manager

A self-hosted web app for keeping an audiobook library organized — the library
side of Radarr/Sonarr, with **no download features**. It scans a folder,
matches books to online metadata (Audible, Google Books, Open Library), and
renames files and folders **in place** to a consistent layout that downstream
tools such as Audiobookshelf expect.

## Features

- Scan one or more library roots; group audio into books (single-file `.m4b`,
  multi-file track folders, `CD1/CD2` disc sets, and `.flac/.ogg/.opus/...`).
- Match against Audible, Google Books, and Open Library. Auto-accept a clear
  winner; review the rest by hand with per-provider search.
- Rename in place to a configurable token template, after reviewing a dry-run
  diff. Every run is journaled and can be undone, including a run that failed
  partway and could not fully roll itself back.
- Optional login (off by default) plus an API key for automation.
- Single static binary with the web UI embedded; Docker image and `.deb`
  package available.

## Folder structures

Per library, one of:

- **Author first** (default): `Author/Series/Book Name (Year)/<files>`
- **Series first**: `Series/Author/Book Name (Year)/<files>` — for multi-author
  anthologies and shared-world series

The `Series` segment is dropped for standalone books. The author folder segment
uses the author's sort name (e.g. `King, Stephen`) when it is known, falling
back to `{author}` otherwise. File names come from an editable token template;
defaults:

- single-file book: `{title}[ ({year})] - {author}{ext}`
- multi-file book: `{title} ({year}) - {track2}{ext}`

Tokens: `{author}` `{author_sort}` `{series}` `{series_index}` `{title}`
`{subtitle}` `{year}` `{narrator}` `{asin}` `{isbn}` `{ext}` `{track}`
`{track2}` `{track3}`. Wrap part of a template in `[ ... ]` to make it optional
— the group disappears when a token inside it is empty (e.g. `{title}[ ({year})]`).

Target paths are kept inside the platform's path limit (259 characters on
Windows, 1024 elsewhere) so the renamed library stays readable by Explorer and
by downstream scanners. Folder names are shortened to fit; file names are never
truncated, and a book whose path cannot be made to fit is skipped with that
reason shown in the preview.

## Running

### Docker

```sh
docker run -d --name audiobookrenamer \
  -p 8674:8674 \
  -v /path/to/config:/config \
  -v /path/to/audiobooks:/audiobooks \
  audiobookrenamer:latest
```

Then open <http://localhost:8674> and add `/audiobooks` as a library root.
The container writes its database to `/config`.

### Native

Prebuilt binaries (Linux, macOS, Windows; amd64/arm64) and a Debian package are
attached to each release. The `.deb` installs a `systemd` unit:

```sh
sudo dpkg -i audiobookrenamer_*_amd64.deb
sudo systemctl enable --now audiobookrenamer   # listens on :8674
```

### From source

Requires Go 1.26+ and Node 20+.

```sh
make build            # builds web/ then the Go binary into dist/
./dist/audiobookrenamer
```

Development (hot-reloading UI on :5173, API on :8674):

```sh
go run ./cmd/audiobookrenamer
cd web && npm run dev
```

## Configuration

Process-level config comes from defaults, an optional JSON file
(`--config path` or `ABR_CONFIG_FILE`), then environment variables:

| Env | Default | Meaning |
|---|---|---|
| `ABR_ADDR` / `ABR_PORT` | `:8674` | HTTP bind address |
| `ABR_CONFIG_DIR` | OS config dir `/audiobookrenamer` (`/config` in Docker) | SQLite DB + local state |
| `ABR_LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error` |

Provider keys, Audible region, the auto-match threshold, and authentication are
set in the **Settings** screen and stored in the database.

## Authentication

Auth is **off by default** — anyone who can reach the port has full access, so
only expose it on a trusted network or behind an authenticating reverse proxy.

To turn it on: Settings → Authentication → set a username and password and
enable it. You will be prompted to sign in. An API key is generated for
automation; send it as `X-Api-Key: <key>` (or `?apikey=<key>` for the job
event stream).

The API key is shown once, in the Settings screen, on the save that generates
it. It is not stored anywhere you can read it back — if you lose it, tick
**Rotate API key on save** for a new one, which invalidates the old.

Changing the username or password signs out every existing session, as does
turning auth off. Behind a TLS-terminating reverse proxy, forward
`X-Forwarded-Proto` so the session cookie is marked `Secure`.

## API sketch

`/api` — `libraries` CRUD + `/{id}/scan` + `/{id}/match`; `books` list/get +
`PATCH /{id}` (hand-edit metadata) + `/{id}/candidates` + `/{id}/match`;
`search`; `browse?path=` (folder picker); `organize/preview` +
`organize/apply`; `jobs` + `/{id}/cancel` + `/{id}/undo` + `/stream` (SSE);
`settings`; `auth/status|login|logout`; `healthz` (unauthenticated).

`browse` lists the folders inside a path on the **server**, which is what the
library-root picker in the UI walks. A browser file input cannot supply a path
the server can open, and in Docker the library lives at a path that does not
exist on the machine running the browser. Like every other `/api` route it sits
behind authentication when auth is enabled; with auth off it lets any caller
read directory names, the same exposure as the existing "add a library at any
path and scan it" flow.

## License

TBD
