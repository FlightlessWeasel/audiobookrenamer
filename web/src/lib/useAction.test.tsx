import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useAction } from "./useAction";
import { deferred } from "../test/fetchMock";

afterEach(() => vi.restoreAllMocks());

function Probe({ fn, actionKey }: { fn: () => Promise<unknown>; actionKey?: string }) {
  const { run, busy, isBusy, error, clearError } = useAction();
  return (
    <div>
      <span data-testid="busy">{busy ? "busy" : "idle"}</span>
      <span data-testid="keyed">{isBusy(actionKey) ? "busy" : "idle"}</span>
      <span data-testid="error">{error ?? "none"}</span>
      <button onClick={() => run(fn, actionKey)}>run</button>
      <button onClick={clearError}>clear</button>
    </div>
  );
}

describe("useAction", () => {
  it("flips busy on for the duration of the action, then off", async () => {
    const d = deferred<void>();
    const user = userEvent.setup();
    render(<Probe fn={() => d.promise} />);

    expect(screen.getByTestId("busy")).toHaveTextContent("idle");
    await user.click(screen.getByText("run"));
    expect(screen.getByTestId("busy")).toHaveTextContent("busy");

    act(() => d.resolve());
    await waitFor(() => expect(screen.getByTestId("busy")).toHaveTextContent("idle"));
  });

  it("captures a thrown error and clears busy", async () => {
    const user = userEvent.setup();
    render(<Probe fn={() => Promise.reject(new Error("boom"))} />);

    await user.click(screen.getByText("run"));

    await waitFor(() => expect(screen.getByTestId("error")).toHaveTextContent("boom"));
    expect(screen.getByTestId("busy")).toHaveTextContent("idle");
  });

  it("clears a captured error on clearError and on the next run", async () => {
    const user = userEvent.setup();
    const { rerender } = render(<Probe fn={() => Promise.reject(new Error("first"))} />);

    await user.click(screen.getByText("run"));
    await waitFor(() => expect(screen.getByTestId("error")).toHaveTextContent("first"));

    await user.click(screen.getByText("clear"));
    expect(screen.getByTestId("error")).toHaveTextContent("none");

    rerender(<Probe fn={() => Promise.reject(new Error("second"))} />);
    await user.click(screen.getByText("run"));
    await waitFor(() => expect(screen.getByTestId("error")).toHaveTextContent("second"));
  });

  it("ignores a second run for a key that is already in flight (sync re-entrancy guard)", async () => {
    const fn = vi.fn(() => deferred<void>().promise);
    render(<Probe fn={fn} actionKey="k" />);

    const btn = screen.getByText("run");
    act(() => {
      btn.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      btn.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    await waitFor(() => expect(screen.getByTestId("keyed")).toHaveTextContent("busy"));
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("tracks independent busy flags per key so one action doesn't clear another's spinner", async () => {
    const da = deferred<void>();
    const db = deferred<void>();
    function TwoKeys() {
      const { run, isBusy } = useAction();
      return (
        <div>
          <span data-testid="a">{isBusy("a") ? "busy" : "idle"}</span>
          <span data-testid="b">{isBusy("b") ? "busy" : "idle"}</span>
          <button onClick={() => run(() => da.promise, "a")}>run-a</button>
          <button onClick={() => run(() => db.promise, "b")}>run-b</button>
        </div>
      );
    }
    const user = userEvent.setup();
    render(<TwoKeys />);

    await user.click(screen.getByText("run-a"));
    await user.click(screen.getByText("run-b"));
    expect(screen.getByTestId("a")).toHaveTextContent("busy");
    expect(screen.getByTestId("b")).toHaveTextContent("busy");

    // Resolving "a" must leave "b" still busy.
    act(() => da.resolve());
    await waitFor(() => expect(screen.getByTestId("a")).toHaveTextContent("idle"));
    expect(screen.getByTestId("b")).toHaveTextContent("busy");

    act(() => db.resolve());
    await waitFor(() => expect(screen.getByTestId("b")).toHaveTextContent("idle"));
  });

  it("does not surface an error from an action that settles after unmount", async () => {
    const d = deferred<void>();
    const errSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const user = userEvent.setup();
    const { unmount } = render(<Probe fn={() => d.promise} />);

    await user.click(screen.getByText("run"));
    unmount();
    act(() => d.reject(new Error("late")));
    await Promise.resolve();

    // No "state update on an unmounted component" error was logged.
    expect(errSpy).not.toHaveBeenCalled();
  });
});
