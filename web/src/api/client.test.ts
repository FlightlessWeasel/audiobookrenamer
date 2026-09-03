import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, client } from "./client";
import { mockFetch } from "../test/fetchMock";

afterEach(() => vi.unstubAllGlobals());

describe("api client", () => {
  it("returns the parsed body on success", async () => {
    mockFetch(() => ({ body: [{ id: "l1", name: "Lib" }] }));
    const libs = await client.listLibraries();
    expect(libs).toHaveLength(1);
    expect(libs[0]).toMatchObject({ id: "l1", name: "Lib" });
  });

  it("wraps a non-JSON gateway error page in ApiError instead of throwing SyntaxError", async () => {
    mockFetch(() => ({ status: 502, statusText: "Bad Gateway", body: "<html>502 Bad Gateway</html>" }));
    const err = await client.listLibraries().catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(502);
    expect(err.message).not.toContain("JSON");
  });

  it("treats a non-JSON 200 body as a malformed response", async () => {
    mockFetch(() => ({ status: 200, body: "not json" }));
    const err = await client.listLibraries().catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(502);
  });

  it("surfaces the server-provided error message", async () => {
    mockFetch(() => ({ status: 400, body: { error: "library_id is required" } }));
    const err = await client.organizePreview("").catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.message).toBe("library_id is required");
  });

  it("dispatches abr:unauthorized on a 401 for a non-auth path", async () => {
    mockFetch(() => ({ status: 401, body: { error: "nope" } }));
    const spy = vi.fn();
    window.addEventListener("abr:unauthorized", spy);
    await client.listBooks().catch(() => {});
    window.removeEventListener("abr:unauthorized", spy);
    expect(spy).toHaveBeenCalledOnce();
  });

  it("does not dispatch abr:unauthorized for an auth path 401", async () => {
    mockFetch(() => ({ status: 401, body: { error: "bad login" } }));
    const spy = vi.fn();
    window.addEventListener("abr:unauthorized", spy);
    await client.login("u", "p").catch(() => {});
    window.removeEventListener("abr:unauthorized", spy);
    expect(spy).not.toHaveBeenCalled();
  });

  it("returns undefined for 204 No Content", async () => {
    mockFetch(() => ({ status: 204 }));
    await expect(client.deleteLibrary("l1")).resolves.toBeUndefined();
  });

  it("forwards an AbortSignal to fetch", async () => {
    const fn = mockFetch(() => ({ body: [] }));
    const ctrl = new AbortController();
    await client.listLibraries({ signal: ctrl.signal });
    expect(fn.mock.calls[0][1]?.signal).toBe(ctrl.signal);
  });
});

describe("response validation", () => {
  it("rejects a list endpoint that returns a non-array", async () => {
    mockFetch(() => ({ body: { not: "an array" } }));
    const err = await client.listLibraries().catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(502);
  });

  it("rejects a library element missing its id", async () => {
    mockFetch(() => ({ body: [{ name: "no id here" }] }));
    const err = await client.listLibraries().catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(502);
  });

  it("rejects a books response whose `books` field is not an array", async () => {
    mockFetch(() => ({ body: { books: "nope", counts: {} } }));
    const err = await client.listBooks().catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(502);
  });

  it("rejects a settings payload missing a required sub-object", async () => {
    mockFetch(() => ({
      body: { auto_match_threshold: 0.8, google_books: {}, open_library: {}, auth: {} }, // no `audible`
    }));
    const err = await client.getSettings().catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(502);
  });

  it("coerces an unknown structure_mode to a safe default (does not throw)", async () => {
    mockFetch(() => ({ body: [{ id: "l1", name: "Lib", structure_mode: "sideways" }] }));
    const libs = await client.listLibraries();
    expect(libs[0].structure_mode).toBe("author_first");
  });

  it("passes an unknown job status through untouched (UI renders it safely)", async () => {
    mockFetch(() => ({
      body: [{ id: "j1", type: "scan", status: "reticulating", total: 0, done: 0, created_at: "" }],
    }));
    const jobs = await client.listJobs();
    expect(jobs[0].status).toBe("reticulating");
  });

  it("keeps every job `type` the Go API emits — including match and undo", async () => {
    mockFetch(() => ({
      body: [
        { id: "j1", type: "scan", status: "done", total: 0, done: 0, created_at: "" },
        { id: "j2", type: "match", status: "running", total: 3, done: 1, created_at: "" },
        { id: "j3", type: "organize", status: "done", total: 0, done: 0, created_at: "" },
        { id: "j4", type: "undo", status: "queued", total: 0, done: 0, created_at: "" },
      ],
    }));
    const jobs = await client.listJobs();
    expect(jobs.map((j) => j.type)).toEqual(["scan", "match", "organize", "undo"]);
  });

  it("does not coerce an unrecognised job `type` to `scan` (keeps it as sent)", async () => {
    mockFetch(() => ({
      body: [{ id: "j1", type: "frobnicate", status: "done", total: 0, done: 0, created_at: "" }],
    }));
    const jobs = await client.listJobs();
    expect(jobs[0].type).toBe("frobnicate" as unknown);
  });

  it("coerces a non-string job `type` to a visible `unknown`, never a real type", async () => {
    mockFetch(() => ({
      body: [{ id: "j1", type: 42, status: "done", total: 0, done: 0, created_at: "" }],
    }));
    const jobs = await client.listJobs();
    expect(jobs[0].type).toBe("unknown" as unknown);
  });

  it("rejects a candidate list element missing provider/title", async () => {
    mockFetch(() => ({ body: [{ provider_id: "x" }] }));
    const err = await client.listCandidates("b1").catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(502);
  });

  it("accepts an organize plan whose skipped book has null moves", async () => {
    mockFetch(() => ({
      body: {
        library_id: "L1",
        root_path: "/lib",
        books: [{ book_id: "b1", title: "X", skip: true, reason: "missing title", moves: null }],
      },
    }));
    const plan = await client.organizePreview("L1", ["b1"]);
    expect(plan.books[0].moves).toEqual([]);
    expect(plan.books[0].skip).toBe(true);
  });

  it("accepts an organize plan with no books array at all", async () => {
    mockFetch(() => ({ body: { library_id: "L1", root_path: "/lib" } }));
    const plan = await client.organizePreview("L1", []);
    expect(plan.books).toEqual([]);
  });
});
