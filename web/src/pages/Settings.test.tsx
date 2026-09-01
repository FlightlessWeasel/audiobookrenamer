import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SettingsPage } from "./Settings";
import { mockFetch } from "../test/fetchMock";

afterEach(() => vi.unstubAllGlobals());

const settings = {
  auto_match_threshold: 0.85,
  audible: { enabled: true, region: "us" },
  google_books: { enabled: false, api_key_set: false },
  open_library: { enabled: true },
  auth: { enabled: false, username: "olduser", api_key_set: false },
};

describe("SettingsPage", () => {
  it("keeps in-progress auth edits when an unrelated provider setting changes", async () => {
    mockFetch((path, init) => {
      if (path === "/settings" && (!init || init.method === undefined)) return { body: settings };
      if (path === "/settings" && init?.method === "PATCH") return { body: settings };
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(<SettingsPage />);

    const username = await screen.findByRole("textbox");
    await waitFor(() => expect(username).toHaveValue("olduser"));

    await user.clear(username);
    await user.type(username, "editing-in-progress");
    expect(username).toHaveValue("editing-in-progress");

    // Toggle an unrelated provider — this mutates the page's `draft` object.
    await user.click(screen.getByRole("checkbox", { name: /enable audible/i }));

    // The auth form must not have been resynced from the server response.
    expect(screen.getByRole("textbox")).toHaveValue("editing-in-progress");
  });

  it("resyncs the auth form from the server response after a successful save", async () => {
    let getCount = 0;
    mockFetch((path, init) => {
      if (path === "/settings" && init?.method !== "PATCH") {
        getCount += 1;
        // Second GET (post-save reload) reports the server's normalised value.
        const username = getCount === 1 ? "olduser" : "server-normalised";
        return { body: { ...settings, auth: { ...settings.auth, username } } };
      }
      if (path === "/settings" && init?.method === "PATCH") {
        return { body: { ...settings, auth: { ...settings.auth, username: "server-normalised" } } };
      }
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    render(<SettingsPage />);

    const username = await screen.findByRole("textbox");
    await waitFor(() => expect(username).toHaveValue("olduser"));

    // Edit then save the auth form.
    await user.clear(username);
    await user.type(username, "something-local");
    await user.click(screen.getByRole("button", { name: /save authentication/i }));

    // After the post-save reload, the field reflects the server's value, not
    // the stale local edit.
    await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue("server-normalised"));
    expect(getCount).toBe(2);
  });
});
