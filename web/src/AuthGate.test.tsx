import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { AuthGate } from "./AuthGate";
import { deferred, mockFetch } from "./test/fetchMock";

afterEach(() => vi.unstubAllGlobals());

const child = <div data-testid="app">secret app</div>;

describe("AuthGate", () => {
  it("renders the app when auth is disabled", async () => {
    mockFetch(() => ({ body: { enabled: false, authenticated: true } }));
    render(<AuthGate>{child}</AuthGate>);
    await waitFor(() => expect(screen.getByTestId("app")).toBeInTheDocument());
  });

  it("shows the login screen when auth is enabled and unauthenticated", async () => {
    mockFetch(() => ({ body: { enabled: true, authenticated: false } }));
    render(<AuthGate>{child}</AuthGate>);
    await waitFor(() => expect(screen.getByRole("button", { name: /sign in/i })).toBeInTheDocument());
    expect(screen.queryByTestId("app")).not.toBeInTheDocument();
  });

  it("fails closed: a failed status request shows login, not the app", async () => {
    mockFetch(() => {
      throw new TypeError("network down");
    });
    render(<AuthGate>{child}</AuthGate>);
    await waitFor(() => expect(screen.getByRole("button", { name: /sign in/i })).toBeInTheDocument());
    expect(screen.queryByTestId("app")).not.toBeInTheDocument();
  });

  it("drops to login when an abr:unauthorized event fires", async () => {
    mockFetch(() => ({ body: { enabled: false, authenticated: true } }));
    render(<AuthGate>{child}</AuthGate>);
    await waitFor(() => expect(screen.getByTestId("app")).toBeInTheDocument());

    act(() => {
      window.dispatchEvent(new CustomEvent("abr:unauthorized"));
    });
    await waitFor(() => expect(screen.getByRole("button", { name: /sign in/i })).toBeInTheDocument());
  });

  it("a slow status response that resolves after abr:unauthorized cannot re-open the app", async () => {
    const d = deferred<{ body: object }>();
    mockFetch(() => d.promise);
    render(<AuthGate>{child}</AuthGate>);

    // Status request is still in flight; an unauthorized event arrives.
    act(() => {
      window.dispatchEvent(new CustomEvent("abr:unauthorized"));
    });
    await waitFor(() => expect(screen.getByRole("button", { name: /sign in/i })).toBeInTheDocument());

    // Now the stale request finally resolves as "authenticated".
    act(() => d.resolve({ body: { enabled: true, authenticated: true } }));
    await Promise.resolve();
    await Promise.resolve();

    // It must be ignored — the login screen stays.
    expect(screen.getByRole("button", { name: /sign in/i })).toBeInTheDocument();
    expect(screen.queryByTestId("app")).not.toBeInTheDocument();
  });

  it("ignores a stale status result even if the aborted fetch still resolves (generation guard)", async () => {
    // A fetch stub that DOES resolve after abort (some transports do), so this
    // exercises AuthGate's `!signal.aborted` guard, not just the abort→reject.
    const d = deferred<Response>();
    vi.stubGlobal(
      "fetch",
      vi.fn(() => d.promise),
    );
    render(<AuthGate>{child}</AuthGate>);

    act(() => window.dispatchEvent(new CustomEvent("abr:unauthorized")));
    await waitFor(() => expect(screen.getByRole("button", { name: /sign in/i })).toBeInTheDocument());

    // The in-flight request resolves — as authenticated — after we've already
    // dropped to login. setStatus must NOT be called with it.
    act(() =>
      d.resolve(
        new Response(JSON.stringify({ enabled: true, authenticated: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(screen.getByRole("button", { name: /sign in/i })).toBeInTheDocument();
    expect(screen.queryByTestId("app")).not.toBeInTheDocument();
  });
});
