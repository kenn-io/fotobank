import { test, expect, type Page, type Request } from "@playwright/test";

// Scale spec: /library at 100k rows, no thumb files. The e2e-server is
// booted with FOTOBANK_E2E_SCALE_ROWS=100000 (see playwright-e2e-scale
// .config.ts), which replaces the curated fixtures with a bulk
// SeedScaleLibrary call. The /thumb endpoint 404s for every cell — the
// SPA renders the broken-image placeholder, and the request count this
// spec measures includes those 404s, which is what we want to track.
//
// The spec is currently SOFT — it logs DOM/heap/request numbers and
// fails only on egregious regressions (DOM > 50k, /api/v1/media >
// 200). PS-6 promotes the stable metrics to hard floors once we have
// spread data across multiple runs.
//
// Output: each capture also gets written to a JSON file under
// frontend/tests/e2e/scale/.snapshots/ so benchstat-style comparisons
// across commits are possible without re-parsing log output.

interface CaptureBefore {
  domNodeCount: number;
  jsHeapUsedSize: number | null;
  apiMediaRequests: number;
  thumbRequests: number;
}

interface CaptureAfterScroll extends CaptureBefore {
  // Same shape; named separately so the diff fields below are obvious.
  pagesScrolled: number;
}

async function captureDomNodeCount(page: Page): Promise<number> {
  return page.evaluate(() => document.querySelectorAll("*").length);
}

// jsHeapUsedSize comes from the V8-only performance.memory API; on
// Chromium it's available unconditionally, but on other browsers the
// property is undefined. The harness runs Chromium-only (per the
// projects[] config) so this should always succeed; we still null-
// guard so a future webkit add-on doesn't crash the spec.
async function captureHeap(page: Page): Promise<number | null> {
  return page.evaluate(() => {
    const m = (performance as unknown as { memory?: { usedJSHeapSize?: number } })
      .memory;
    return m?.usedJSHeapSize ?? null;
  });
}

test.describe("/library scale (100k rows, no thumb files)", () => {
  test("DOM, heap, request counts before + after 10-page scroll", async ({
    page,
  }) => {
    // Tally requests by URL pattern for the whole test so we can
    // diff before/after counts. Two predicates:
    //   - /api/v1/media?...  — paged list calls fired by mediaStore
    //   - /thumb/...         — broken-thumb fetches (every cell 404s)
    let apiMediaCount = 0;
    let thumbCount = 0;
    page.on("request", (req: Request) => {
      const url = req.url();
      const path = new URL(url).pathname;
      if (path === "/api/v1/media") apiMediaCount += 1;
      // Thumb endpoint is /api/v1/media/<id>/thumb (huma path
      // /api/v1/media/:id/thumb in server routing). The trailing
      // segment match avoids /api/v1/media itself collision-counting.
      if (path.endsWith("/thumb") && path.startsWith("/api/v1/media/")) {
        thumbCount += 1;
      }
    });

    // Initial paint. waitForResponse on the first /api/v1/media call so
    // the captures below run against a steady-state grid, not a
    // half-rendered one.
    await Promise.all([
      page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname === "/api/v1/media" && r.status() === 200,
      ),
      page.goto("/"),
    ]);
    // Library is the default route ("/"). Wait for the first cell to
    // mount so the IntersectionObserver and VirtualGrid are wired.
    await expect(
      page.locator("[data-media-id]").first(),
    ).toBeVisible({ timeout: 10_000 });

    const before: CaptureBefore = {
      domNodeCount: await captureDomNodeCount(page),
      jsHeapUsedSize: await captureHeap(page),
      apiMediaRequests: apiMediaCount,
      thumbRequests: thumbCount,
    };
    console.log("[scale/library] BEFORE", JSON.stringify(before));

    // Scroll-to-bottom 10 times. The scroll container is .main (not
    // window) and VirtualGrid's loadMore IntersectionObserver fires
    // when its bottom-of-content sentinel enters the viewport (default
    // root) within rootMargin=800px. With 200-row pages each loaded
    // page adds ~10k pixels of content, so a single viewport scroll
    // (~800px) is nowhere near enough to bring the sentinel into
    // range — only scrolling to the bottom reliably trips loadMore.
    //
    // Each iteration: scroll to the new scrollHeight, wait for the
    // /api/v1/media response. mediaStore exhaustion (next_offset:null)
    // would cause the wait to time out — the spec is sized for 100k
    // rows / 200-per-page = 500 page budget, so 10 iterations stay
    // well within the store's reach.
    const PAGES = 10;
    for (let i = 0; i < PAGES; i++) {
      await Promise.all([
        page.waitForResponse(
          (r) =>
            new URL(r.url()).pathname === "/api/v1/media" &&
            r.status() === 200,
          { timeout: 30_000 },
        ),
        page.evaluate(() => {
          const main = document.querySelector(".main");
          if (main) main.scrollTo(0, main.scrollHeight);
        }),
      ]);
    }

    const after: CaptureAfterScroll = {
      domNodeCount: await captureDomNodeCount(page),
      jsHeapUsedSize: await captureHeap(page),
      apiMediaRequests: apiMediaCount,
      thumbRequests: thumbCount,
      pagesScrolled: PAGES,
    };
    console.log("[scale/library] AFTER ", JSON.stringify(after));

    // Soft floors. These are sized to fail on a 5-10x regression vs
    // current observed values, not on tight thresholds. PS-6 will
    // tighten them after we have a stable baseline.
    //
    //   DOM nodes: virtualization should keep the live tree well
    //   under 50k even after 10 pages of scroll. A regression in
    //   VirtualGrid that drops virtualization (rendering all cells)
    //   would push past this fast.
    expect(after.domNodeCount).toBeLessThan(50_000);
    //
    //   /api/v1/media requests: 1 initial + 10 scroll loads + a few
    //   filter-change retries = generous ceiling 50. A regression
    //   that fires a request per cell (mediaStore loop bug) would
    //   blow past this.
    expect(after.apiMediaRequests).toBeLessThan(50);
    //
    //   /thumb requests: every visible cell + recently-scrolled-out
    //   cells fire one request each. With virtualization keeping
    //   ~50-200 cells alive at once and ~10 pages of scroll fan-out,
    //   ~2000 is the upper bound observed; 5000 is a generous floor.
    //   A regression that re-fetches on every scroll step would
    //   exceed this.
    expect(after.thumbRequests).toBeLessThan(5_000);

    // Capture file written to disk so future runs can diff. The path
    // is intentionally outside test fixtures (a runtime artifact, not
    // a regression baseline) — PS-6 promotes the chosen metrics to
    // hard floors at a different layer.
    const fs = await import("node:fs/promises");
    const path = await import("node:path");
    const dir = path.resolve(".snapshots");
    await fs.mkdir(dir, { recursive: true });
    const stamp = new Date().toISOString().replace(/[:.]/g, "-");
    await fs.writeFile(
      path.join(dir, `library-${stamp}.json`),
      JSON.stringify({ before, after }, null, 2),
    );
  });
});
