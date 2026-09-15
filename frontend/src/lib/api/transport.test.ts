import { describe, expect, it, vi } from "vitest";
import { search } from "./generated/client";

describe("generated client transport", () => {
  it("sends repeated search filters and preserves cancellation", async () => {
    const controller = new AbortController();
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
      new Response(JSON.stringify({ results: [], has_more: false })),
    );

    const result = await search(
      { q: "beach & sea", tag: ["family", "summer"], include_hidden: false },
      { baseUrl: "https://fotobank.example", fetch, signal: controller.signal },
    );

    const [url, options] = fetch.mock.calls[0]!;
    const requestURL = new URL(String(url));
    expect(requestURL.pathname).toBe("/api/v1/search");
    expect(requestURL.searchParams.get("q")).toBe("beach & sea");
    expect(requestURL.searchParams.getAll("tag")).toEqual(["family", "summer"]);
    expect(requestURL.searchParams.get("include_hidden")).toBe("false");
    expect(options?.signal).toBe(controller.signal);
    expect(result.data?.results).toEqual([]);
  });

  it("returns HTTP problem details without treating them as success data", async () => {
    const problem = { status: 403, detail: "Unlock hidden media first" };
    const response = new Response(JSON.stringify(problem), { status: 403 });
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(response);

    const result = await search({ include_hidden: true }, { fetch });

    expect(result.data).toBeUndefined();
    expect(result.error).toEqual(problem);
    expect(result.response).toBe(response);
  });
});
