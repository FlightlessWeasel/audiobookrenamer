import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { OrganizePage } from "./Organize";
import { mockFetch } from "../test/fetchMock";

afterEach(() => vi.unstubAllGlobals());

const libs = [
  { id: "A", name: "Library A" },
  { id: "B", name: "Library B" },
];

function book(id: string, library_id: string) {
  return { id, library_id, layout: "single", state: "matched", title: `Book ${id}`, updated_at: "" };
}

describe("OrganizePage grouping", () => {
  it("groups the selection list by author, sorted by author, with per-group counts", async () => {
    mockFetch((path) => {
      if (path === "/libraries") return { body: libs };
      if (path.startsWith("/books")) {
        return {
          body: {
            books: [
              { ...book("a1", "A"), author: "Zed Author" },
              { ...book("a2", "A"), author: "Amy Author" },
              { ...book("a3", "A"), author: "Amy Author" },
              { ...book("a4", "A"), author: "" },
            ],
            counts: {},
          },
        };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(<OrganizePage />);
    await waitFor(() => expect(screen.getByText("4 of 4 matched books")).toBeInTheDocument());

    await user.selectOptions(screen.getByRole("combobox", { name: /group books by/i }), "author");

    const groupHeaders = screen
      .getAllByRole("columnheader")
      .filter((h) => h.getAttribute("colspan") === "2");
    expect(groupHeaders.map((h) => h.textContent)).toEqual([
      "Amy Author2",
      "Unknown author1",
      "Zed Author1",
    ]);
    // Grouping is display-only: every book is still checked and selectable.
    expect(screen.getByRole("checkbox", { name: /select book a4/i })).toBeChecked();
  });
});

describe("OrganizePage bulk selection", () => {
  it("toggles every book with the top-level select-all checkbox", async () => {
    mockFetch((path) => {
      if (path === "/libraries") return { body: libs };
      if (path.startsWith("/books")) {
        return { body: { books: [book("a1", "A"), book("a2", "A"), book("a3", "A")], counts: {} } };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(<OrganizePage />);
    await waitFor(() => expect(screen.getByText("3 of 3 matched books")).toBeInTheDocument());

    const selectAll = screen.getByRole("checkbox", { name: /select all books/i });
    expect(selectAll).toBeChecked();

    await user.click(selectAll);
    await waitFor(() => expect(screen.getByText("0 of 3 matched books")).toBeInTheDocument());
    expect(screen.getByRole("checkbox", { name: /select book a2/i })).not.toBeChecked();

    await user.click(selectAll);
    await waitFor(() => expect(screen.getByText("3 of 3 matched books")).toBeInTheDocument());
  });

  it("selects and clears a single group with its group checkbox", async () => {
    mockFetch((path) => {
      if (path === "/libraries") return { body: libs };
      if (path.startsWith("/books")) {
        return {
          body: {
            books: [
              { ...book("a1", "A"), author: "Amy Author" },
              { ...book("a2", "A"), author: "Amy Author" },
              { ...book("a3", "A"), author: "Zed Author" },
            ],
            counts: {},
          },
        };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(<OrganizePage />);
    await waitFor(() => expect(screen.getByText("3 of 3 matched books")).toBeInTheDocument());
    await user.selectOptions(screen.getByRole("combobox", { name: /group books by/i }), "author");

    const amyGroup = screen.getByRole("checkbox", { name: /select all in amy author/i });
    expect(amyGroup).toBeChecked();

    await user.click(amyGroup);
    await waitFor(() => expect(screen.getByText("1 of 3 matched books")).toBeInTheDocument());
    expect(screen.getByRole("checkbox", { name: /select book a1/i })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: /select book a3/i })).toBeChecked();

    await user.click(amyGroup);
    await waitFor(() => expect(screen.getByText("3 of 3 matched books")).toBeInTheDocument());
  });

  it("shows the matching provider and score for each book", async () => {
    mockFetch((path) => {
      if (path === "/libraries") return { body: libs };
      if (path.startsWith("/books")) {
        return {
          body: {
            books: [
              { ...book("a1", "A"), matched_provider: "audible", match_score: 0.92 },
              { ...book("a2", "A") },
            ],
            counts: {},
          },
        };
      }
      return { status: 404, body: { error: "nope" } };
    });

    render(<OrganizePage />);
    await waitFor(() => expect(screen.getByText("2 of 2 matched books")).toBeInTheDocument());

    expect(screen.getByText("audible")).toBeInTheDocument();
    expect(screen.getByText("92%")).toBeInTheDocument();
    expect(screen.getByText("no match info")).toBeInTheDocument();
  });
});

describe("OrganizePage library switching", () => {
  it("clears the selection when the library changes so stale ids can't be applied", async () => {
    let releaseB: (() => void) | null = null;
    mockFetch((path) => {
      if (path === "/libraries") return { body: libs };
      if (path.startsWith("/books")) {
        const lib = new URLSearchParams(path.split("?")[1]).get("library_id");
        if (lib === "A") return { body: { books: [book("a1", "A"), book("a2", "A")], counts: {} } };
        // Library B: hold the response open until the test releases it.
        return new Promise((resolve) => {
          releaseB = () => resolve({ body: { books: [book("b1", "B")], counts: {} } } as never);
        });
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(<OrganizePage />);

    // Library A loads and auto-selects both books.
    await waitFor(() => expect(screen.getByText("2 of 2 matched books")).toBeInTheDocument());

    await user.selectOptions(screen.getByRole("combobox", { name: /organize library/i }), "B");

    // While B is still loading, A's rows are gone and Preview is disabled, so
    // A's ids can't be submitted against library B.
    await waitFor(() => expect(screen.getByText("loading books…")).toBeInTheDocument());
    expect(screen.queryByText("Book a1")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /preview/i })).toBeDisabled();

    // Once B resolves, its single book is selected — never A's ids.
    releaseB!();
    await waitFor(() => expect(screen.getByText("1 of 1 matched books")).toBeInTheDocument());
  });

  it("preserves the user's selection across a reload (e.g. after Apply)", async () => {
    mockFetch((path) => {
      if (path === "/libraries") return { body: libs };
      if (path.startsWith("/books")) return { body: { books: [book("a1", "A"), book("a2", "A")], counts: {} } };
      if (path === "/organize/preview") {
        return {
          body: {
            library_id: "A",
            root_path: "/x",
            books: [
              { book_id: "a2", title: "Book a2", skip: false, moves: [{ from_rel: "a", to_rel: "b", no_op: false }] },
            ],
          },
        };
      }
      if (path === "/organize/apply") {
        return { body: { id: "job1", type: "organize", status: "queued", total: 0, done: 0, created_at: "" } };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(<OrganizePage />);
    await waitFor(() => expect(screen.getByText("2 of 2 matched books")).toBeInTheDocument());

    await user.click(screen.getByRole("checkbox", { name: /select book a1/i }));
    await waitFor(() => expect(screen.getByText("1 of 2 matched books")).toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: /preview/i }));
    await user.click(await screen.findByRole("button", { name: /apply/i }));

    // Back on the selection view after the post-apply reload: a1 must still be
    // unchecked, not reset to "all selected".
    await waitFor(() => expect(screen.getByText(/Organize job queued/i)).toBeInTheDocument());
    expect(screen.getByRole("checkbox", { name: /select book a1/i })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: /select book a2/i })).toBeChecked();
    expect(screen.getByText("1 of 2 matched books")).toBeInTheDocument();
  });

  it("sends only the selected ids to organizePreview", async () => {
    const calls: Array<{ path: string; body: unknown }> = [];
    mockFetch((path, init) => {
      if (path === "/libraries") return { body: libs };
      if (path.startsWith("/books")) return { body: { books: [book("a1", "A"), book("a2", "A")], counts: {} } };
      if (path === "/organize/preview") {
        calls.push({ path, body: JSON.parse(String(init?.body)) });
        return { body: { library_id: "A", root_path: "/x", books: [] } };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(<OrganizePage />);
    await waitFor(() => expect(screen.getByText("2 of 2 matched books")).toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: /preview/i }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].body).toEqual({ library_id: "A", book_ids: ["a1", "a2"] });
  });
});

describe("OrganizePage all-no-op plan", () => {
  // A book that is already sitting at its correct organized path (rematched to
  // metadata that renders the same location) plans with zero real moves - every
  // move is no_op, but the book is not skipped. Apply must still be enabled: it
  // is what flips the book's status back to organized.
  it("enables Apply when the plan has no real moves but a non-skipped book", async () => {
    let applyCalled = false;
    mockFetch((path, init) => {
      if (path === "/libraries") return { body: libs };
      if (path.startsWith("/books")) return { body: { books: [book("a1", "A")], counts: {} } };
      if (path === "/organize/preview") {
        return {
          body: {
            library_id: "A",
            root_path: "/x",
            books: [
              {
                book_id: "a1",
                title: "Book a1",
                skip: false,
                moves: [{ from_rel: "a", to_rel: "a", no_op: true }],
              },
            ],
          },
        };
      }
      if (path === "/organize/apply" && init?.method === "POST") {
        applyCalled = true;
        return { body: { id: "job1", type: "organize", status: "queued", total: 0, done: 0, created_at: "" } };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(<OrganizePage />);
    await waitFor(() => expect(screen.getByText("1 of 1 matched books")).toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: /preview/i }));
    const applyButton = await screen.findByRole("button", { name: /apply/i });

    await waitFor(() => expect(applyButton).toBeEnabled());
    expect(
      screen.getByText(/no files need to move.*update these books' status to organized/i),
    ).toBeInTheDocument();

    await user.click(applyButton);
    await waitFor(() => expect(applyCalled).toBe(true));
  });

  it("disables Apply and explains when every selected book was actually skipped", async () => {
    mockFetch((path) => {
      if (path === "/libraries") return { body: libs };
      if (path.startsWith("/books")) return { body: { books: [book("a1", "A")], counts: {} } };
      if (path === "/organize/preview") {
        return {
          body: {
            library_id: "A",
            root_path: "/x",
            books: [{ book_id: "a1", title: "Book a1", skip: true, reason: "missing title", moves: [] }],
          },
        };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(<OrganizePage />);
    await waitFor(() => expect(screen.getByText("1 of 1 matched books")).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: /preview/i }));

    await waitFor(() =>
      expect(screen.getByText("Nothing to do — every selected book was skipped.")).toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: /apply/i })).toBeDisabled();
  });
});
