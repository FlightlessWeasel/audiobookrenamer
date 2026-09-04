import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LibraryPage } from "./Library";
import { mockFetch } from "../test/fetchMock";

afterEach(() => vi.unstubAllGlobals());

const lib = {
  id: "L1",
  name: "Lib One",
  root_path: "/books",
  structure_mode: "author_first",
  file_template: "{title}{ext}",
  multi_file_template: "{title} - {track2}{ext}",
  enabled: true,
  created_at: "",
  updated_at: "",
};

describe("LibraryPage edit form", () => {
  it("submits an updated file_template via PATCH /libraries/{id}", async () => {
    let patchBody: unknown = null;
    mockFetch((path, init) => {
      if (path === "/libraries" && (!init || init.method === undefined)) return { body: [lib] };
      if (path === "/libraries/L1" && init?.method === "PATCH") {
        patchBody = JSON.parse(String(init?.body));
        return { body: { ...lib, file_template: "{author} - {title}{ext}" } };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(<LibraryPage />);

    await user.click(await screen.findByRole("button", { name: /^edit$/i }));

    const tmpl = screen.getByRole("textbox", { name: /single-file template/i });
    await user.clear(tmpl);
    await user.type(tmpl, "{{author} - {{title}{{ext}");

    await user.click(screen.getByRole("button", { name: /save changes/i }));

    await waitFor(() => expect(patchBody).not.toBeNull());
    expect(patchBody).toMatchObject({ file_template: "{author} - {title}{ext}" });
  });

  it("rescan-all POSTs /libraries/rescan-all and reports the queued count", async () => {
    let called = false;
    mockFetch((path, init) => {
      if (path === "/libraries" && (!init || init.method === undefined)) return { body: [lib] };
      if (path === "/libraries/rescan-all" && init?.method === "POST") {
        called = true;
        return { status: 202, body: { jobs: [{ id: "j1", type: "scan", status: "queued", total: 0, done: 0, created_at: "" }] } };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(<LibraryPage />);

    await user.click(await screen.findByRole("button", { name: /rescan all/i }));

    await waitFor(() => expect(called).toBe(true));
    await screen.findByText(/queued 1 scan/i);
  });

  it("sends file_template:\"\" when the template field is cleared (reset to default)", async () => {
    let patchBody: Record<string, unknown> = {};
    mockFetch((path, init) => {
      if (path === "/libraries" && (!init || init.method === undefined)) return { body: [lib] };
      if (path === "/libraries/L1" && init?.method === "PATCH") {
        patchBody = JSON.parse(String(init?.body));
        return { body: lib };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(<LibraryPage />);

    await user.click(await screen.findByRole("button", { name: /^edit$/i }));
    await user.clear(screen.getByRole("textbox", { name: /single-file template/i }));
    await user.click(screen.getByRole("button", { name: /save changes/i }));

    await waitFor(() => expect("file_template" in patchBody).toBe(true));
    expect(patchBody.file_template).toBe("");
  });
});
