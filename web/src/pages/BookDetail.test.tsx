import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { BookDetailPage } from "./BookDetail";
import { mockFetch } from "../test/fetchMock";

afterEach(() => vi.unstubAllGlobals());

const book = {
  id: "b1",
  library_id: "L1",
  source_dir: "/books/Dune",
  layout: "single",
  state: "matched",
  title: "Dune",
  author: "Frank Herbert",
  author_sort: "Herbert, Frank",
  author_sort_source: "derived",
  updated_at: "",
};

const settings = {
  auto_match_threshold: 0.85,
  audible: { enabled: false, region: "us" },
  google_books: { enabled: false, api_key_set: false },
  open_library: { enabled: true },
  auth: { enabled: false, api_key_set: false },
};

function renderBookDetail() {
  return render(
    <MemoryRouter initialEntries={["/books/b1"]}>
      <Routes>
        <Route path="/books/:id" element={<BookDetailPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("BookDetail manual search", () => {
  it("puts the chosen provider in the /search request body", async () => {
    let searchBody: Record<string, unknown> = {};
    mockFetch((path, init) => {
      if (path === "/books/b1" && (!init || init.method === undefined)) return { body: book };
      if (path === "/books/b1/candidates") return { body: [] };
      if (path === "/settings") return { body: settings };
      if (path === "/search" && init?.method === "POST") {
        searchBody = JSON.parse(String(init?.body));
        return { body: [] };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    renderBookDetail();

    const select = await screen.findByRole("combobox", { name: /provider/i });
    await waitFor(() => expect(screen.getByRole("option", { name: "Open Library" })).toBeInTheDocument());
    await user.selectOptions(select, "openlibrary");

    await user.click(screen.getByRole("button", { name: /search providers/i }));

    await waitFor(() => expect(searchBody.provider).toBe("openlibrary"));
  });
});

describe("BookDetail author sort", () => {
  it("PATCHes /books/{id} with the edited author_sort", async () => {
    let patchBody: Record<string, unknown> | null = null;
    mockFetch((path, init) => {
      if (path === "/books/b1" && init?.method === "PATCH") {
        patchBody = JSON.parse(String(init?.body));
        return { body: { ...book, author_sort: "Le Guin, Ursula K.", author_sort_source: "manual" } };
      }
      if (path === "/books/b1" && (!init || init.method === undefined)) return { body: book };
      if (path === "/books/b1/candidates") return { body: [] };
      if (path === "/settings") return { body: settings };
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    renderBookDetail();

    const input = await screen.findByRole("textbox", { name: /author sort name/i });
    await user.clear(input);
    await user.type(input, "Le Guin, Ursula K.");
    await user.click(screen.getByRole("button", { name: /save sort name/i }));

    await waitFor(() => expect(patchBody).not.toBeNull());
    expect(patchBody).toEqual({ author_sort: "Le Guin, Ursula K." });
  });
});

// A book matched to a provider carries series/year/narrator/ASIN. The manual
// search+entry form is a full replacement of the book's metadata, so every
// field it can submit must start from what the book already has — otherwise
// "Accept these fields" silently wipes them. This wiped the series of every
// Audible match the user then hand-adjusted.
describe("BookDetail manual entry preserves existing metadata", () => {
  const matched = {
    ...book,
    title: "Tarnished Knight",
    author: "Jack Campbell",
    subtitle: "The Lost Stars, Book 1",
    narrator: "Marc Vietor",
    series: "The Lost Stars",
    series_index: "1",
    year: 2012,
    asin: "B009C4BLS0",
    isbn: "9781101601396",
    cover_url: "https://example.test/cover.jpg",
    matched_provider: "audible",
  };

  function mountMatched(capture: (body: Record<string, unknown>) => void) {
    mockFetch((path, init) => {
      if (path === "/books/b1" && init?.method === "POST") {
        throw new Error("unexpected POST to /books/b1");
      }
      if (path === "/books/b1/match" && init?.method === "POST") {
        capture(JSON.parse(String(init?.body)));
        return { body: { book: matched } };
      }
      if (path === "/books/b1" && (!init || init.method === undefined)) return { body: matched };
      if (path === "/books/b1/candidates") return { body: [] };
      if (path === "/settings") return { body: settings };
      return { status: 404, body: { error: "nope" } };
    });
    return renderBookDetail();
  }

  it("pre-fills the series fields from the book", async () => {
    mountMatched(() => {});
    const series = await screen.findByRole("textbox", { name: /^series$/i });
    expect(series).toHaveValue("The Lost Stars");
    expect(screen.getByRole("textbox", { name: /series #/i })).toHaveValue("1");
    expect(screen.getByRole("textbox", { name: /^year$/i })).toHaveValue("2012");
  });

  it("keeps series and the fields it does not show when accepting", async () => {
    let sent: Record<string, unknown> = {};
    mountMatched((b) => {
      sent = b;
    });

    const user = userEvent.setup();
    await screen.findByRole("textbox", { name: /^series$/i });
    await user.click(screen.getByRole("button", { name: /accept these fields/i }));

    await waitFor(() => expect(sent.manual).toBeDefined());
    const manual = sent.manual as Record<string, unknown>;
    expect(manual.series).toBe("The Lost Stars");
    expect(manual.series_index).toBe("1");
    expect(manual.year).toBe(2012);
    // Not shown by the form, so they can only survive by being carried through.
    expect(manual.subtitle).toBe("The Lost Stars, Book 1");
    expect(manual.narrator).toBe("Marc Vietor");
    expect(manual.asin).toBe("B009C4BLS0");
    expect(manual.isbn).toBe("9781101601396");
    expect(manual.cover_url).toBe("https://example.test/cover.jpg");
  });
});

describe("BookDetail organize panel", () => {
  it("waits for the organize job to finish, then reloads so the book shows organized", async () => {
    const calls: string[] = [];
    // The book only flips to organized once the background job completes; the
    // panel must wait for that, not read back the pre-run "matched".
    let bookState = "matched";
    mockFetch((path, init) => {
      calls.push(`${init?.method ?? "GET"} ${path}`);
      if (path === "/books/b1" && (!init || init.method === undefined))
        return { body: { ...book, state: bookState } };
      if (path === "/books/b1/candidates") return { body: [] };
      if (path === "/settings") return { body: settings };
      if (path === "/organize/preview" && init?.method === "POST") {
        return {
          body: {
            library_id: "L1",
            root_path: "/lib",
            books: [
              {
                book_id: "b1",
                title: "Dune",
                skip: false,
                moves: [{ from_rel: "Dune/old.m4b", to_rel: "Herbert, Frank/Dune/Dune.m4b", no_op: false }],
              },
            ],
          },
        };
      }
      if (path === "/organize/apply" && init?.method === "POST") {
        return { body: { id: "job1", type: "organize", status: "queued", total: 0, done: 0, created_at: "" } };
      }
      if (path === "/jobs/job1") {
        // The job has finished and the book row has been updated server-side.
        bookState = "organized";
        return { body: { id: "job1", type: "organize", status: "done", total: 1, done: 1, created_at: "" } };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    renderBookDetail();

    await user.click(await screen.findByRole("button", { name: /preview rename/i }));
    expect(await screen.findByText("Herbert, Frank/Dune/Dune.m4b")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /^apply$/i }));

    await waitFor(() =>
      expect(screen.getByText(/this book is now organized/i)).toBeInTheDocument(),
    );
    // The metadata reloaded and now reads the organized state.
    expect(screen.getAllByText("organized").length).toBeGreaterThanOrEqual(1);
    // It polled the job and only reloaded the book after apply, not before.
    expect(calls).toContain("GET /jobs/job1");
    expect(calls.filter((c) => c === "GET /books/b1").length).toBeGreaterThanOrEqual(2);
  });

  it("surfaces a failed organize job instead of claiming success", async () => {
    mockFetch((path, init) => {
      if (path === "/books/b1" && (!init || init.method === undefined)) return { body: book };
      if (path === "/books/b1/candidates") return { body: [] };
      if (path === "/settings") return { body: settings };
      if (path === "/organize/preview" && init?.method === "POST") {
        return {
          body: {
            library_id: "L1",
            root_path: "/lib",
            books: [
              {
                book_id: "b1",
                title: "Dune",
                skip: false,
                moves: [{ from_rel: "a", to_rel: "b", no_op: false }],
              },
            ],
          },
        };
      }
      if (path === "/organize/apply" && init?.method === "POST") {
        return { body: { id: "job1", type: "organize", status: "queued", total: 0, done: 0, created_at: "" } };
      }
      if (path === "/jobs/job1") {
        return {
          body: { id: "job1", type: "organize", status: "failed", error: "disk full", total: 1, done: 0, created_at: "" },
        };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    renderBookDetail();

    await user.click(await screen.findByRole("button", { name: /preview rename/i }));
    await user.click(await screen.findByRole("button", { name: /^apply$/i }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/disk full/i);
    expect(screen.queryByText(/now organized/i)).not.toBeInTheDocument();
  });

  it("cannot organize a book that isn't matched yet", async () => {
    mockFetch((path, init) => {
      if (path === "/books/b1" && (!init || init.method === undefined))
        return { body: { ...book, state: "unmatched" } };
      if (path === "/books/b1/candidates") return { body: [] };
      if (path === "/settings") return { body: settings };
      return { status: 404, body: { error: "nope" } };
    });

    renderBookDetail();

    expect(await screen.findByText(/Match this book before organizing/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /preview rename/i })).toBeDisabled();
  });
});

describe("BookDetail delete", () => {
  it("DELETEs the book and navigates back to the list once confirmed", async () => {
    vi.stubGlobal("confirm", () => true);
    let deleteCalled = false;
    mockFetch((path, init) => {
      if (path === "/books/b1" && (!init || init.method === undefined)) return { body: book };
      if (path === "/books/b1/candidates") return { body: [] };
      if (path === "/settings") return { body: settings };
      if (path === "/books/b1" && init?.method === "DELETE") {
        deleteCalled = true;
        return { status: 204 };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/books/b1"]}>
        <Routes>
          <Route path="/books/:id" element={<BookDetailPage />} />
          <Route path="/books" element={<div>book list</div>} />
        </Routes>
      </MemoryRouter>,
    );

    await user.click(await screen.findByRole("button", { name: /^delete$/i }));

    await waitFor(() => expect(deleteCalled).toBe(true));
    expect(await screen.findByText("book list")).toBeInTheDocument();
  });

  it("does nothing when the confirm is dismissed", async () => {
    vi.stubGlobal("confirm", () => false);
    const fetchFn = mockFetch((path, init) => {
      if (path === "/books/b1" && (!init || init.method === undefined)) return { body: book };
      if (path === "/books/b1/candidates") return { body: [] };
      if (path === "/settings") return { body: settings };
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    renderBookDetail();

    await user.click(await screen.findByRole("button", { name: /^delete$/i }));

    expect(
      fetchFn.mock.calls.some(([u, i]) => String(u).endsWith("/books/b1") && (i as RequestInit)?.method === "DELETE"),
    ).toBe(false);
  });
});

describe("BookDetail tag status", () => {
  it("checks this book's tags on demand and shows the per-file detail", async () => {
    let tagStatusBody: unknown = null;
    mockFetch((path, init) => {
      if (path === "/books/b1" && (!init || init.method === undefined)) return { body: book };
      if (path === "/books/b1/candidates") return { body: [] };
      if (path === "/settings") return { body: settings };
      if (path === "/books/tag-status" && init?.method === "POST") {
        tagStatusBody = JSON.parse(String(init.body));
        return {
          body: {
            books: [
              {
                id: "b1",
                enabled: true,
                match: "mismatch",
                files: [{ file_rel: "Dune.m4b", writable: true, changed: true }],
              },
            ],
          },
        };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    renderBookDetail();

    // Not fetched automatically on page load.
    await screen.findByText(/embedded tags/i);
    expect(screen.queryByText("tags differ")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /^check tags$/i }));

    await screen.findByText("tags differ");
    // The per-file breakdown ("Dune.m4b — out of date") is its own list item,
    // distinct from the summary sentence above it that also names the file.
    expect(screen.getByRole("listitem")).toHaveTextContent("Dune.m4b");
    expect(tagStatusBody).toEqual({ ids: ["b1"] });
  });
});
