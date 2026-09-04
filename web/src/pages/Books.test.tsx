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
    const groupHeaders = headers.filter((h) => h.getAttribute("colspan") === "7");
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
      screen.getAllByRole("columnheader").filter((h) => h.getAttribute("colspan") === "7").length,
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
        screen.getAllByRole("columnheader").filter((h) => h.getAttribute("colspan") === "7").map((h) => h.textContent),
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
