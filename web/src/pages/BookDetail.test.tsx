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
  it("previews and applies the rename for this one book", async () => {
    const calls: Array<{ path: string; body: unknown }> = [];
    mockFetch((path, init) => {
      if (path === "/books/b1" && (!init || init.method === undefined)) return { body: book };
      if (path === "/books/b1/candidates") return { body: [] };
      if (path === "/settings") return { body: settings };
      if (path === "/organize/preview" && init?.method === "POST") {
        calls.push({ path, body: JSON.parse(String(init?.body)) });
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
        calls.push({ path, body: JSON.parse(String(init?.body)) });
        return { body: { id: "job1", type: "organize", status: "queued", total: 0, done: 0, created_at: "" } };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    renderBookDetail();

    await user.click(await screen.findByRole("button", { name: /preview rename/i }));

    expect(await screen.findByText("Herbert, Frank/Dune/Dune.m4b")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /^apply$/i }));

    await waitFor(() => expect(screen.getByText(/Organize job queued/i)).toBeInTheDocument());
    expect(calls.map((c) => [c.path, c.body])).toEqual([
      ["/organize/preview", { library_id: "L1", book_ids: ["b1"] }],
      ["/organize/apply", { library_id: "L1", book_ids: ["b1"] }],
    ]);
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
