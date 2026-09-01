import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { App } from "./App";
import { AuthGate } from "./AuthGate";
import { deferred, mockFetch, type MockResponse } from "./test/fetchMock";

afterEach(() => vi.unstubAllGlobals());

// `Ctx` isn't exported from AuthGate, so drive the real tree: a mocked
// /auth/status makes AuthGate render its children (App) with a populated
// context.
function renderApp() {
  return render(
    <MemoryRouter initialEntries={["/library"]}>
      <AuthGate>
        <Routes>
          <Route path="/" element={<App />}>
            <Route path="library" element={<div data-testid="home" />} />
          </Route>
        </Routes>
      </AuthGate>
    </MemoryRouter>,
  );
}

describe("App sign-out", () => {
  it("disables the button while logging out and shows a role=alert error on failure", async () => {
    const logout = deferred<MockResponse>();
    mockFetch((path, init) => {
      if (path === "/auth/status") {
        return { body: { enabled: true, authenticated: true, username: "u" } };
      }
      if (path === "/auth/logout" && init?.method === "POST") return logout.promise;
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    renderApp();

    const btn = await screen.findByRole("button", { name: /sign out \(u\)/i });
    await user.click(btn);

    // In flight → disabled.
    expect(screen.getByRole("button", { name: /sign out \(u\)/i })).toBeDisabled();

    // Logout fails.
    act(() => logout.resolve({ status: 500, body: { error: "logout blew up" } }));

    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(/logout blew up/i));
    expect(screen.getByRole("button", { name: /sign out \(u\)/i })).toBeEnabled();
  });

  it("clears the pending state after a successful logout", async () => {
    const logout = deferred<MockResponse>();
    let statusCalls = 0;
    mockFetch((path, init) => {
      if (path === "/auth/status") {
        statusCalls += 1;
        return { body: { enabled: true, authenticated: true, username: "u" } };
      }
      if (path === "/auth/logout" && init?.method === "POST") return logout.promise;
      return { status: 404, body: { error: "nope" } };
    });

    const user = userEvent.setup();
    renderApp();

    const btn = await screen.findByRole("button", { name: /sign out \(u\)/i });
    await user.click(btn);
    expect(screen.getByRole("button", { name: /sign out \(u\)/i })).toBeDisabled();

    act(() => logout.resolve({ status: 200, body: { ok: true } }));

    // refresh() re-hits /auth/status, and the button re-enables.
    await waitFor(() => expect(screen.getByRole("button", { name: /sign out \(u\)/i })).toBeEnabled());
    expect(statusCalls).toBeGreaterThanOrEqual(2);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
