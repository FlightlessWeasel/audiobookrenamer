import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { useAsync } from "./useAsync";

afterEach(() => vi.restoreAllMocks());

function Probe({ fn, deps }: { fn: (s: AbortSignal) => Promise<string>; deps: unknown[] }) {
  const { data, error, loading } = useAsync(fn, deps);
  return (
    <div>
      <span data-testid="state">{loading ? "loading" : (error ?? data ?? "empty")}</span>
    </div>
  );
}

describe("useAsync", () => {
  it("resolves data", async () => {
    render(<Probe fn={async () => "hello"} deps={[]} />);
    await waitFor(() => expect(screen.getByTestId("state")).toHaveTextContent("hello"));
  });

  it("aborts the in-flight request when deps change and ignores its late result", async () => {
    const signals: AbortSignal[] = [];
    const fn = (s: AbortSignal) =>
      new Promise<string>((resolve) => {
        signals.push(s);
        setTimeout(() => resolve(`v${signals.length}`), 10);
      });

    const { rerender } = render(<Probe fn={fn} deps={[1]} />);
    rerender(<Probe fn={fn} deps={[2]} />);

    await waitFor(() => expect(screen.getByTestId("state")).toHaveTextContent("v2"));
    expect(signals[0].aborted).toBe(true);
    expect(signals[1].aborted).toBe(false);
  });

  it("does not surface an AbortError as an error", async () => {
    const fn = (s: AbortSignal) =>
      new Promise<string>((_resolve, reject) => {
        s.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        setTimeout(() => _resolve("late"), 50);
      });

    const { rerender } = render(<Probe fn={fn} deps={[1]} />);
    rerender(<Probe fn={fn} deps={[2]} />);
    // give the first promise time to reject
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.getByTestId("state")).not.toHaveTextContent("aborted");
  });
});
