import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RootPathField } from "./FolderPicker";
import { mockFetch } from "../test/fetchMock";

afterEach(() => vi.unstubAllGlobals());

// The picker walks the server's filesystem, so descending has to fetch each
// level and the chosen path must be the server-side absolute path — not
// anything derived in the browser.
function mockTree() {
  return mockFetch((path) => {
    if (path.startsWith("/browse?path=")) {
      const at = decodeURIComponent(path.slice("/browse?path=".length));
      if (at === "") {
        return { body: { path: "", parent: "", entries: [{ name: "/", path: "/" }] } };
      }
      if (at === "/") {
        return {
          body: {
            path: "/",
            parent: "",
            entries: [
              { name: "mnt", path: "/mnt" },
              { name: "srv", path: "/srv" },
            ],
          },
        };
      }
      if (at === "/mnt") {
        return {
          body: {
            path: "/mnt",
            parent: "/",
            entries: [{ name: "audiobooks", path: "/mnt/audiobooks" }],
          },
        };
      }
      return { body: { path: at, parent: "/mnt", entries: [] } };
    }
    return { status: 404, body: { error: "nope" } };
  });
}

describe("RootPathField", () => {
  it("browses the server filesystem and fills the field with the chosen path", async () => {
    mockTree();
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<RootPathField value="" onChange={onChange} />);

    await user.click(screen.getByRole("button", { name: /browse/i }));

    // Root listing, then descend two levels.
    await user.click(await screen.findByRole("button", { name: "📁 /" }));
    await user.click(await screen.findByRole("button", { name: "📁 mnt" }));
    await user.click(await screen.findByRole("button", { name: "📁 audiobooks" }));

    await waitFor(() =>
      expect(screen.getByText("/mnt/audiobooks")).toBeInTheDocument(),
    );
    await user.click(screen.getByRole("button", { name: /use this folder/i }));

    expect(onChange).toHaveBeenCalledWith("/mnt/audiobooks");
    // The dialog closes on pick.
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("still accepts a typed path, so a hidden folder stays reachable", async () => {
    mockTree();
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<RootPathField value="" onChange={onChange} />);

    await user.type(screen.getByRole("textbox", { name: /root path/i }), "/x");

    expect(onChange).toHaveBeenCalled();
  });

  it("cancelling leaves the field untouched", async () => {
    mockTree();
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<RootPathField value="/existing" onChange={onChange} />);

    await user.click(screen.getByRole("button", { name: /browse/i }));
    await screen.findByRole("dialog");
    await user.click(screen.getByRole("button", { name: /cancel/i }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
  });
});
