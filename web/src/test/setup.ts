// Vitest global setup: jest-dom matchers, and jsdom shims for browser APIs the
// app uses that jsdom doesn't implement (EventSource, structuredClone).
import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

afterEach(() => cleanup());

if (typeof globalThis.structuredClone !== "function") {
  globalThis.structuredClone = (v: unknown) => JSON.parse(JSON.stringify(v));
}

// Minimal EventSource stand-in. Tests that care about SSE behaviour install
// their own fake; this just keeps modules that construct one at import/mount
// time from throwing.
if (typeof (globalThis as { EventSource?: unknown }).EventSource === "undefined") {
  class FakeEventSource {
    url: string;
    onerror: ((e: unknown) => void) | null = null;
    onmessage: ((e: unknown) => void) | null = null;
    constructor(url: string) {
      this.url = url;
    }
    addEventListener() {}
    removeEventListener() {}
    close() {}
  }
  (globalThis as { EventSource?: unknown }).EventSource = FakeEventSource;
}
