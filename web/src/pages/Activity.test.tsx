import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ActivityPage } from "./Activity";
import { deferred, mockFetch } from "../test/fetchMock";

type Listener = (e: { data: string }) => void;

class FakeEventSource {
  static last: FakeEventSource | null = null;
  listeners: Record<string, Listener[]> = {};
  onerror: (() => void) | null = null;
  closed = false;
  constructor(public url: string) {
    FakeEventSource.last = this;
  }
  addEventListener(type: string, fn: Listener) {
    (this.listeners[type] ??= []).push(fn);
  }
  removeEventListener() {}
  close() {
    this.closed = true;
  }
  emit(type: string, data: string) {
    (this.listeners[type] ?? []).forEach((fn) => fn({ data }));
  }
}

afterEach(() => vi.unstubAllGlobals());

const oneJob = [
  {
    id: "j1",
    type: "organize",
    status: "running",
    total: 0,
    done: 0,
    created_at: new Date().toISOString(),
  },
];

describe("ActivityPage SSE handling", () => {
  it("ignores a malformed SSE frame without crashing", async () => {
    mockFetch(() => ({ body: oneJob }));
    vi.stubGlobal("EventSource", FakeEventSource);
    render(<ActivityPage />);
    await waitFor(() => expect(screen.getByText("organize")).toBeInTheDocument());

    act(() => FakeEventSource.last!.emit("job", "{ this is not json"));

    // Still rendered, no error boundary / thrown exception.
    expect(screen.getByText("organize")).toBeInTheDocument();
  });

  it("applies a progress frame to the matching job row", async () => {
    mockFetch(() => ({ body: oneJob }));
    vi.stubGlobal("EventSource", FakeEventSource);
    render(<ActivityPage />);
    await waitFor(() => expect(screen.getByText("organize")).toBeInTheDocument());

    act(() =>
      FakeEventSource.last!.emit(
        "job",
        JSON.stringify({ job_id: "j1", type: "progress", total: 10, done: 3, message: "moving" }),
      ),
    );

    await waitFor(() => expect(screen.getByText(/3\/10/)).toBeInTheDocument());
    expect(screen.getByText(/moving/)).toBeInTheDocument();
  });

  it("renders an unknown job status without an undefined class", async () => {
    mockFetch(() => ({ body: oneJob }));
    vi.stubGlobal("EventSource", FakeEventSource);
    const { container } = render(<ActivityPage />);
    await waitFor(() => expect(screen.getByText("organize")).toBeInTheDocument());

    act(() =>
      FakeEventSource.last!.emit("job", JSON.stringify({ job_id: "j1", type: "weird-state" })),
    );

    await waitFor(() => expect(screen.getByText("weird-state")).toBeInTheDocument());
    expect(container.querySelector('[class*="undefined"]')).toBeNull();
  });

  it("caps the live-overlay map so a flood of distinct job ids can't grow it unbounded", async () => {
    // Five real jobs, all "queued" in the fetched list.
    const base = Array.from({ length: 5 }, (_, i) => ({
      id: `base-${i}`,
      type: "scan",
      status: "queued",
      total: 0,
      done: 0,
      created_at: new Date().toISOString(),
    }));
    mockFetch(() => ({ body: base }));
    vi.stubGlobal("EventSource", FakeEventSource);
    render(<ActivityPage />);
    await waitFor(() => expect(screen.getAllByText("scan")).toHaveLength(5));

    // Overlay all five with a "running" live event → their badges flip.
    act(() => {
      for (let i = 0; i < 5; i++) {
        FakeEventSource.last!.emit("job", JSON.stringify({ job_id: `base-${i}`, type: "running" }));
      }
    });
    await waitFor(() => expect(screen.getAllByText("running")).toHaveLength(5));

    // Now flood with 300 events for ids that never appear in the fetched list.
    act(() => {
      for (let i = 0; i < 300; i++) {
        FakeEventSource.last!.emit("job", JSON.stringify({ job_id: `flood-${i}`, type: "running" }));
      }
    });

    // The oldest overlay entries (base-0..4) were evicted by the cap, so those
    // rows fall back to their fetched "queued" status. No crash, bounded map.
    await waitFor(() => expect(screen.getAllByText("queued")).toHaveLength(5));
    expect(screen.queryByText("running")).not.toBeInTheDocument();
  });

  it("surfaces a banner when the SSE stream errors and clears it on reconnect", async () => {
    mockFetch(() => ({ body: oneJob }));
    vi.stubGlobal("EventSource", FakeEventSource);
    render(<ActivityPage />);
    await waitFor(() => expect(screen.getByText("organize")).toBeInTheDocument());

    expect(screen.queryByText(/live updates disconnected/i)).not.toBeInTheDocument();

    act(() => FakeEventSource.last!.onerror?.());
    await waitFor(() =>
      expect(screen.getByText(/live updates disconnected/i)).toBeInTheDocument(),
    );

    act(() => FakeEventSource.last!.emit("open", ""));
    await waitFor(() =>
      expect(screen.queryByText(/live updates disconnected/i)).not.toBeInTheDocument(),
    );
  });

  it("refetches authoritative job state when the server signals a reconcile", async () => {
    let jobListCalls = 0;
    mockFetch((path) => {
      if (path.startsWith("/jobs?")) jobListCalls++;
      return { body: oneJob };
    });
    vi.stubGlobal("EventSource", FakeEventSource);
    render(<ActivityPage />);
    await waitFor(() => expect(screen.getByText("organize")).toBeInTheDocument());

    const before = jobListCalls;
    act(() => FakeEventSource.last!.emit("reconcile", "{}"));

    await waitFor(() => expect(jobListCalls).toBeGreaterThan(before));
  });

  it("does not fire a second cancel request on rapid double-click", async () => {
    const d = deferred<{ status: number; body: object }>();
    let cancelCalls = 0;
    mockFetch((path) => {
      if (path === "/jobs/j1/cancel") {
        cancelCalls++;
        return d.promise;
      }
      return { body: oneJob };
    });
    vi.stubGlobal("EventSource", FakeEventSource);
    render(<ActivityPage />);

    const cancel = await screen.findByRole("button", { name: /cancel/i });

    // Two clicks dispatched inside a single act() block, before React can
    // re-render the button into its disabled state. A synchronous guard must
    // stop the second one reaching the network.
    act(() => {
      cancel.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      cancel.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /cancelling/i })).toBeDisabled(),
    );
    expect(cancelCalls).toBe(1);

    act(() => d.resolve({ status: 200, body: {} }));
    await waitFor(() => expect(screen.getByRole("button", { name: /^cancel$/i })).toBeEnabled());
  });

  it("shows a pending Cancel button while the request is in flight and surfaces a rejection", async () => {
    const d = deferred<{ status: number; body: object }>();
    mockFetch((path) => {
      if (path === "/jobs/j1/cancel") return d.promise;
      return { body: oneJob };
    });
    vi.stubGlobal("EventSource", FakeEventSource);
    const user = userEvent.setup();
    render(<ActivityPage />);

    const cancel = await screen.findByRole("button", { name: /cancel/i });
    await user.click(cancel);

    // In flight: disabled + pending label.
    expect(screen.getByRole("button", { name: /cancelling/i })).toBeDisabled();

    // Reject the action.
    act(() => d.resolve({ status: 500, body: { error: "cancel exploded" } }));

    await waitFor(() => expect(screen.getByText(/cancel exploded/i)).toBeInTheDocument());
    expect(screen.getByRole("button", { name: /^cancel$/i })).toBeEnabled();
  });
});
