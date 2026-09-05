import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { BooksPage } from "./Books";
import { mockFetch } from "../test/fetchMock";

afterEach(() => vi.unstubAllGlobals());

const libs = [
  { id: "L1", name: "Lib One", root_path: "/one", structure_mode: "author_first", file_template: "", multi_file_template: "", enabled: true, created_at: "", updated_at: "" },
  { id: "L2", name: "Lib Two", root_path: "/two", structure_mode: "author_first", file_template: "", multi_file_template: "", enabled: true, created_at: "", updated_at: "" },
];

const books = [
  { id: "b1", library_id: "L1", title: "Alpha", author: "Zed Author", state: "matched", layout: "single", source_file: "/one/a.m4b" },
  { id: "b2", library_id: "L2", title: "Bravo", author: "Amy Author", state: "unmatched", layout: "single", source_file: "/two/b.m4b" },
  { id: "b3", library_id: "L1", title: "Charlie", author: "Amy Author", state: "matched", layout: "single", source_file: "/one/c.m4b" },
  { id: "b4", library_id: "L2", title: "Delta", author: "", state: "matched", layout: "single", source_file: "/two/d.m4b" },
];

function mount() {
  mockFetch((path) => {
    if (path === "/libraries") return { body: libs };
    if (path.startsWith("/books")) return { body: { books, counts: {} } };
    return { status: 404, body: { error: "nope" } };
  });
  return render(
    <MemoryRouter>
      <BooksPage />
    </MemoryRouter>,
  );
}

describe("BooksPage grouping", () => {
  it("renders a flat list with no group headers by default", async () => {
    mount();
    expect(await screen.findByRole("link", { name: "Alpha" })).toBeInTheDocument();
    expect(screen.queryByRole("columnheader", { name: /Amy Author/ })).not.toBeInTheDocument();
  });

  it("groups rows by author, sorted by author name, with per-group counts", async () => {
    mount();
    await screen.findByRole("link", { name: "Alpha" });

    await userEvent.setup().selectOptions(
      screen.getByRole("combobox", { name: /group books by/i }),
      "author",
    );

    const headers = await screen.findAllByRole("columnheader");
    const groupHeaders = headers.filter((h) => h.getAttribute("colspan") === "8");
    expect(groupHeaders.map((h) => h.textContent)).toEqual([
      "Amy Author2",
      "Unknown author1",
      "Zed Author1",
    ]);
  });

  it("remembers the group-by choice across a remount (tab switch / reload)", async () => {
    const first = mount();
    await screen.findByRole("link", { name: "Alpha" });
    await userEvent.setup().selectOptions(
      screen.getByRole("combobox", { name: /group books by/i }),
      "author",
    );
    first.unmount();
    cleanup();

    mount();
    await screen.findByRole("link", { name: "Alpha" });
    expect(screen.getByRole("combobox", { name: /group books by/i })).toHaveValue("author");
    expect(
      screen.getAllByRole("columnheader").filter((h) => h.getAttribute("colspan") === "8").length,
    ).toBeGreaterThan(0);
  });

  it("groups rows by library", async () => {
    mount();
    await screen.findByRole("link", { name: "Alpha" });

    await userEvent.setup().selectOptions(
      screen.getByRole("combobox", { name: /group books by/i }),
      "library",
    );

    await waitFor(() =>
      expect(
        screen.getAllByRole("columnheader").filter((h) => h.getAttribute("colspan") === "8").map((h) => h.textContent),
      ).toEqual(["Lib One2", "Lib Two2"]),
    );
  });
});

describe("BooksPage bulk delete", () => {
  it("posts the selected ids to /books/delete and reloads", async () => {
    vi.stubGlobal("confirm", () => true);
    const fetchFn = mockFetch((path) => {
      if (path === "/libraries") return { body: libs };
      if (path === "/books/delete") return { body: { deleted: 2 } };
      if (path.startsWith("/books")) return { body: { books, counts: {} } };
      return { status: 404, body: { error: "nope" } };
    });
    render(
      <MemoryRouter>
        <BooksPage />
      </MemoryRouter>,
    );

    const user = userEvent.setup();
    await screen.findByRole("link", { name: "Alpha" });

    await user.click(screen.getByRole("checkbox", { name: "Select Alpha" }));
    await user.click(screen.getByRole("checkbox", { name: "Select Bravo" }));
    await user.click(screen.getByRole("button", { name: /delete selected/i }));

    await waitFor(() => {
      const call = fetchFn.mock.calls.find(([u]) => String(u).endsWith("/books/delete"));
      expect(call).toBeTruthy();
      expect(JSON.parse(call![1]!.body as string)).toEqual({ ids: ["b1", "b2"] });
    });
    await screen.findByText("Deleted 2 books.");
  });

  it("does not call the endpoint when the confirm is dismissed", async () => {
    vi.stubGlobal("confirm", () => false);
    const fetchFn = mockFetch((path) => {
      if (path === "/libraries") return { body: libs };
      if (path.startsWith("/books")) return { body: { books, counts: {} } };
      return { status: 404, body: { error: "nope" } };
    });
    render(
      <MemoryRouter>
        <BooksPage />
      </MemoryRouter>,
    );
    const user = userEvent.setup();
    await screen.findByRole("link", { name: "Alpha" });
    await user.click(screen.getByRole("checkbox", { name: "Select Alpha" }));
    await user.click(screen.getByRole("button", { name: /delete selected/i }));

    expect(fetchFn.mock.calls.some(([u]) => String(u).endsWith("/books/delete"))).toBe(false);
  });
});

describe("BooksPage bulk retag", () => {
  it("retags selected books via organize/apply, skipping unmatched ones, and re-checks their tags", async () => {
    vi.stubGlobal("confirm", () => true);
    const applyBodies: { library_id: string; book_ids: string[] }[] = [];
    let tagStatusBody: unknown = null;
    mockFetch((path, init) => {
      if (path === "/libraries") return { body: libs };
      if (path === "/organize/apply" && init?.method === "POST") {
        applyBodies.push(JSON.parse(String(init.body)));
        return { body: { id: "job1", type: "organize", status: "queued", total: 0, done: 0, created_at: "" } };
      }
      if (path === "/jobs/job1") {
        return { body: { id: "job1", type: "organize", status: "done", total: 1, done: 1, created_at: "" } };
      }
      if (path === "/books/tag-status" && init?.method === "POST") {
        tagStatusBody = JSON.parse(String(init.body));
        const { ids } = tagStatusBody as { ids: string[] };
        return { body: { books: ids.map((id) => ({ id, enabled: true, match: "match" })) } };
      }
      if (path.startsWith("/books")) return { body: { books, counts: {} } };
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <BooksPage />
      </MemoryRouter>,
    );
    await screen.findByRole("link", { name: "Alpha" });

    // b1 (L1, matched), b2 (L2, unmatched — must be skipped), b3 (L1, matched)
    await user.click(screen.getByRole("checkbox", { name: "Select Alpha" }));
    await user.click(screen.getByRole("checkbox", { name: "Select Bravo" }));
    await user.click(screen.getByRole("checkbox", { name: "Select Charlie" }));
    await user.click(screen.getByRole("button", { name: /retag selected/i }));

    await screen.findByText(/retagged 2 books \(1 skipped — not matched\)\./i);
    expect(applyBodies).toEqual([{ library_id: "L1", book_ids: ["b1", "b3"] }]);
    expect(tagStatusBody).toEqual({ ids: ["b1", "b3"] });
  });

  it("groups a cross-library selection into one organize/apply call per library", async () => {
    vi.stubGlobal("confirm", () => true);
    const applyBodies: { library_id: string; book_ids: string[] }[] = [];
    mockFetch((path, init) => {
      if (path === "/libraries") return { body: libs };
      if (path === "/organize/apply" && init?.method === "POST") {
        applyBodies.push(JSON.parse(String(init.body)));
        return {
          body: { id: `job${applyBodies.length}`, type: "organize", status: "queued", total: 0, done: 0, created_at: "" },
        };
      }
      if (path.startsWith("/jobs/job")) {
        return {
          body: { id: path.split("/").pop(), type: "organize", status: "done", total: 1, done: 1, created_at: "" },
        };
      }
      if (path === "/books/tag-status" && init?.method === "POST") {
        const { ids } = JSON.parse(String(init.body)) as { ids: string[] };
        return { body: { books: ids.map((id) => ({ id, enabled: true, match: "match" })) } };
      }
      if (path.startsWith("/books")) return { body: { books, counts: {} } };
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <BooksPage />
      </MemoryRouter>,
    );
    await screen.findByRole("link", { name: "Alpha" });

    // b1 (L1, matched) and b4 (L2, matched) span two libraries.
    await user.click(screen.getByRole("checkbox", { name: "Select Alpha" }));
    await user.click(screen.getByRole("checkbox", { name: "Select Delta" }));
    await user.click(screen.getByRole("button", { name: /retag selected/i }));

    await screen.findByText("Retagged 2 books.");
    expect(applyBodies).toEqual([
      { library_id: "L1", book_ids: ["b1"] },
      { library_id: "L2", book_ids: ["b4"] },
    ]);
    // The selection clears once the action settles, same as bulk delete.
    expect(screen.getByRole("checkbox", { name: "Select Alpha" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Select Delta" })).not.toBeChecked();
  });

  it("reports book counts, not library counts, when one library's job fails and another succeeds", async () => {
    vi.stubGlobal("confirm", () => true);
    mockFetch((path, init) => {
      if (path === "/libraries") return { body: libs };
      if (path === "/organize/apply" && init?.method === "POST") {
        const { library_id } = JSON.parse(String(init.body)) as { library_id: string };
        return {
          body: {
            id: library_id === "L1" ? "job-ok" : "job-bad",
            type: "organize",
            status: "queued",
            total: 0,
            done: 0,
            created_at: "",
          },
        };
      }
      if (path === "/jobs/job-ok") {
        return { body: { id: "job-ok", type: "organize", status: "done", total: 1, done: 1, created_at: "" } };
      }
      if (path === "/jobs/job-bad") {
        return {
          body: { id: "job-bad", type: "organize", status: "failed", error: "disk full", total: 1, done: 0, created_at: "" },
        };
      }
      if (path === "/books/tag-status" && init?.method === "POST") {
        const { ids } = JSON.parse(String(init.body)) as { ids: string[] };
        return { body: { books: ids.map((id) => ({ id, enabled: true, match: "match" })) } };
      }
      if (path.startsWith("/books")) return { body: { books, counts: {} } };
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <BooksPage />
      </MemoryRouter>,
    );
    await screen.findByRole("link", { name: "Alpha" });

    // b1 (L1, matched) and b3 (L1, matched) succeed; b4 (L2, matched) fails —
    // 3 books total across 2 library jobs.
    await user.click(screen.getByRole("checkbox", { name: "Select Alpha" }));
    await user.click(screen.getByRole("checkbox", { name: "Select Charlie" }));
    await user.click(screen.getByRole("checkbox", { name: "Select Delta" }));
    await user.click(screen.getByRole("button", { name: /retag selected/i }));

    // Book-level, not library-level: 2 of 3 books, not "1 of 2 libraries".
    await screen.findByText("Retagged 2 of 3 books; 1 failed (disk full).");
  });

  it("does nothing when none of the selection is retaggable", async () => {
    vi.stubGlobal("confirm", () => true);
    const fetchFn = mockFetch((path) => {
      if (path === "/libraries") return { body: libs };
      if (path.startsWith("/books")) return { body: { books, counts: {} } };
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <BooksPage />
      </MemoryRouter>,
    );
    await screen.findByRole("link", { name: "Alpha" });

    // b2 is unmatched — the only book selected.
    await user.click(screen.getByRole("checkbox", { name: "Select Bravo" }));
    await user.click(screen.getByRole("button", { name: /retag selected/i }));

    await screen.findByText(/none of the selected books can be retagged/i);
    expect(fetchFn.mock.calls.some(([u]) => String(u).endsWith("/organize/apply"))).toBe(false);
  });
});

describe("BooksPage tag status", () => {
  it("posts every listed id to /books/tag-status and renders a badge per row", async () => {
    let tagStatusBody: unknown = null;
    mockFetch((path, init) => {
      if (path === "/libraries") return { body: libs };
      if (path === "/books/tag-status" && init?.method === "POST") {
        tagStatusBody = JSON.parse(String(init.body));
        return {
          body: {
            books: [
              { id: "b1", enabled: true, match: "mismatch", files: [{ file_rel: "a.m4b", writable: true, changed: true }] },
              { id: "b3", enabled: true, match: "match" },
            ],
          },
        };
      }
      if (path.startsWith("/books")) return { body: { books, counts: {} } };
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <BooksPage />
      </MemoryRouter>,
    );
    await screen.findByRole("link", { name: "Alpha" });
    // No indicator until the check has actually run.
    expect(screen.queryByText("tags differ")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /check tags/i }));

    await screen.findByText("tags differ");
    expect(screen.getByText("tags match")).toBeInTheDocument();
    expect(tagStatusBody).toEqual({ ids: ["b1", "b2", "b3", "b4"] });
  });

  it("disables Check tags when there is nothing listed", async () => {
    mockFetch((path) => {
      if (path === "/libraries") return { body: libs };
      if (path.startsWith("/books")) return { body: { books: [], counts: {} } };
      return { status: 404, body: { error: "nope" } };
    });
    render(
      <MemoryRouter>
        <BooksPage />
      </MemoryRouter>,
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /check tags/i })).toBeDisabled(),
    );
  });

  it("stays enabled and checks in batches for a list bigger than one request can carry", async () => {
    // One over the server's per-request id cap: a single library this size
    // used to disable the button outright with no way to check any of it.
    const manyBooks = Array.from({ length: 201 }, (_, i) => ({
      id: `m${i}`,
      library_id: "L1",
      title: `Book ${i}`,
      author: "Author",
      state: "matched",
      layout: "single",
      source_file: `/one/book${i}.m4b`,
    }));
    const requestedBatches: string[][] = [];
    mockFetch((path, init) => {
      if (path === "/libraries") return { body: libs };
      if (path === "/books/tag-status" && init?.method === "POST") {
        const { ids } = JSON.parse(String(init.body)) as { ids: string[] };
        requestedBatches.push(ids);
        return { body: { books: ids.map((id) => ({ id, enabled: true, match: "match" })) } };
      }
      if (path.startsWith("/books")) return { body: { books: manyBooks, counts: {} } };
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <BooksPage />
      </MemoryRouter>,
    );
    await screen.findByRole("link", { name: "Book 0" });

    const button = screen.getByRole("button", { name: /check tags/i });
    expect(button).toBeEnabled();
    await user.click(button);

    await waitFor(() => expect(requestedBatches).toHaveLength(2));
    expect(requestedBatches[0]).toHaveLength(200);
    expect(requestedBatches[1]).toHaveLength(1);
    expect(await screen.findAllByText("tags match")).toHaveLength(201);
  });
});
