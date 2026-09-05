import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SettingsPage } from "./Settings";
import { mockFetch } from "../test/fetchMock";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const settings = {
  auto_match_threshold: 0.85,
  audible: { enabled: true, region: "us" },
  google_books: { enabled: false, api_key_set: false },
  open_library: { enabled: true },
  auth: { enabled: false, username: "olduser", api_key_set: false },
};

const noUpdate = {
  current: "v1.2.3",
  latest: "",
  has_update: false,
  notes: "",
  url: "",
  can_apply: true,
  reason: "",
  checked_at: "2026-09-05T12:00:00Z",
};

describe("SettingsPage", () => {
  it("keeps in-progress auth edits when an unrelated provider setting changes", async () => {
    mockFetch((path, init) => {
      if (path === "/settings" && (!init || init.method === undefined)) return { body: settings };
      if (path === "/settings" && init?.method === "PATCH") return { body: settings };
      if (path === "/update") return { body: noUpdate };
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
      if (path === "/update") return { body: noUpdate };
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

const updateAvailable = {
  current: "v1.2.3",
  latest: "v1.3.0",
  has_update: true,
  notes: "## Fixes\n- fixed a thing",
  url: "https://github.com/x/y/releases/tag/v1.3.0",
  can_apply: true,
  reason: "",
  checked_at: "2026-09-05T12:00:00Z",
};

describe("SettingsPage — updates", () => {
  it("shows the new version, release notes and an Update button when an update is available", async () => {
    mockFetch((path) => {
      if (path === "/settings") return { body: settings };
      if (path === "/update") return { body: updateAvailable };
      return { status: 404, body: { error: "nope" } };
    });

    render(<SettingsPage />);

    expect(await screen.findByText("v1.3.0")).toBeInTheDocument();
    expect(screen.getByText(/fixed a thing/)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /update & restart/i }),
    ).toBeInTheDocument();
  });

  it("shows an up-to-date message when there is no update", async () => {
    mockFetch((path) => {
      if (path === "/settings") return { body: settings };
      if (path === "/update") return { body: { ...noUpdate, current: "v1.3.0" } };
      return { status: 404, body: { error: "nope" } };
    });

    render(<SettingsPage />);

    expect(
      await screen.findByText(/on the latest version/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /update & restart/i }),
    ).not.toBeInTheDocument();
  });

  it("shows the reason instead of an Update button when self-update is not possible", async () => {
    mockFetch((path) => {
      if (path === "/settings") return { body: settings };
      if (path === "/update")
        return {
          body: {
            ...updateAvailable,
            can_apply: false,
            reason: "cannot self-update a container build",
          },
        };
      return { status: 404, body: { error: "nope" } };
    });

    render(<SettingsPage />);

    expect(
      await screen.findByText(/cannot self-update a container build/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /update & restart/i }),
    ).not.toBeInTheDocument();
  });

  it("runs the apply flow and reloads once the server reports the new version", async () => {
    const origLocation = Object.getOwnPropertyDescriptor(window, "location");
    const reload = vi.fn();
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { ...window.location, reload },
    });
    vi.spyOn(window, "confirm").mockReturnValue(true);

    mockFetch((path, init) => {
      if (path === "/settings") return { body: settings };
      if (path === "/update") return { body: updateAvailable };
      if (path === "/update/apply" && init?.method === "POST")
        return {
          status: 202,
          body: { id: "j1", type: "selfupdate", status: "queued", total: 0, done: 0, created_at: "" },
        };
      if (path === "/jobs/j1")
        return {
          body: { id: "j1", type: "selfupdate", status: "done", total: 1, done: 1, created_at: "" },
        };
      if (path === "/healthz")
        return { body: { status: "ok", time: "", version: "v1.3.0" } };
      return { status: 404, body: { error: "nope" } };
    });

    try {
      const user = userEvent.setup();
      render(<SettingsPage />);

      await user.click(
        await screen.findByRole("button", { name: /update & restart/i }),
      );
      await screen.findByText(/restarting on v1\.3\.0/i);

      await waitFor(() => expect(reload).toHaveBeenCalled(), { timeout: 5000 });
    } finally {
      if (origLocation) Object.defineProperty(window, "location", origLocation);
    }
  }, 10000);

  it("still reaches the restart wait when the job poll fails mid-restart", async () => {
    const origLocation = Object.getOwnPropertyDescriptor(window, "location");
    const reload = vi.fn();
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { ...window.location, reload },
    });
    vi.spyOn(window, "confirm").mockReturnValue(true);

    mockFetch((path, init) => {
      if (path === "/settings") return { body: settings };
      if (path === "/update") return { body: updateAvailable };
      if (path === "/update/apply" && init?.method === "POST")
        return {
          status: 202,
          body: { id: "j1", type: "selfupdate", status: "queued", total: 0, done: 0, created_at: "" },
        };
      // The server re-execs before the poll lands, so getJob fails outright.
      if (path === "/jobs/j1") throw new TypeError("Failed to fetch");
      if (path === "/healthz")
        return { body: { status: "ok", time: "", version: "v1.3.0" } };
      return { status: 404, body: { error: "nope" } };
    });

    try {
      const user = userEvent.setup();
      render(<SettingsPage />);

      await user.click(
        await screen.findByRole("button", { name: /update & restart/i }),
      );
      await screen.findByText(/restarting on v1\.3\.0/i);
      await waitFor(() => expect(reload).toHaveBeenCalled(), { timeout: 5000 });
      expect(
        screen.queryByText(/failed to fetch/i),
      ).not.toBeInTheDocument();
    } finally {
      if (origLocation) Object.defineProperty(window, "location", origLocation);
    }
  }, 10000);
});
