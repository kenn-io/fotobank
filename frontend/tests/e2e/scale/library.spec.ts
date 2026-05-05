import { test, expect, type Page, type Request } from "@playwright/test";

// Scale spec: /library at 100k rows, no thumb files. The e2e-server is
// booted with FOTOBANK_E2E_SCALE_ROWS=100000 (see playwright-e2e-scale
// .config.ts), which replaces the curated fixtures with a bulk
// SeedScaleLibrary call. The /thumb endpoint 404s for every cell — the
// SPA renders the broken-image placeholder, and the request count this
// spec measures includes those 404s, which is what we want to track.
//
// PS-3c instrumentation:
//
// - Per-scroll samples: data-month count, media-cell count, DOM nodes,
//   CDP listeners captured after EACH of the 10 scrolls, not just
//   before/after totals. Per-scroll deltas reveal whether chunks
//   accumulate (linear growth) or recycle (plateau).
//
// - rAF frame intervals: a window-installed rAF loop pushes
//   performance.now() deltas into an array during the scroll loop.
//   median + p95 are the paint-cost signal; longtask count is a
//   secondary signal (longtask only fires for >50ms tasks, so its
//   absence isn't proof of smooth scrolling).
//
// - A/B variant: a second test runs with `.month, .cell {
//   content-visibility: visible !important }` injected via
//   page.addStyleTag, defeating the chunk-level optimization. Compare
//   frame intervals across the two variants to confirm the c-v change
//   actually moves a number.
//
// Soft floors only at this stage; PS-6 promotes stable metrics to
// hard assertions once spread data is collected.

interface DOMCounters {
  documents: number;
  nodes: number;
  jsEventListeners: number;
}

interface PerScrollSample {
  iter: number;
  dataMonthCount: number;
  mediaCellCount: number;
  domNodeCount: number;
  cdpNodes: number;
  cdpJSEventListeners: number;
  apiMediaRequests: number;
  thumbRequests: number;
}

interface FrameStats {
  frameCount: number;
  durationMs: number;
  medianFrameMs: number;
  p95FrameMs: number;
  maxFrameMs: number;
  longtaskCount: number;
}

interface ScaleResult {
  variant: string;
  initial: PerScrollSample;
  perScroll: PerScrollSample[];
  scrollFrames: FrameStats;
  pagesScrolled: number;
}

async function captureSample(
  page: Page,
  cdp: import("@playwright/test").CDPSession,
  iter: number,
  apiMediaCount: () => number,
  thumbCount: () => number,
): Promise<PerScrollSample> {
  const counts = await page.evaluate(() => ({
    dataMonthCount: document.querySelectorAll("[data-month]").length,
    mediaCellCount: document.querySelectorAll("[data-media-id]").length,
    domNodeCount: document.querySelectorAll("*").length,
  }));
  const cdpCounters = (await cdp.send("Memory.getDOMCounters")) as DOMCounters;
  return {
    iter,
    dataMonthCount: counts.dataMonthCount,
    mediaCellCount: counts.mediaCellCount,
    domNodeCount: counts.domNodeCount,
    cdpNodes: cdpCounters.nodes,
    cdpJSEventListeners: cdpCounters.jsEventListeners,
    apiMediaRequests: apiMediaCount(),
    thumbRequests: thumbCount(),
  };
}

// Install a rAF instrument on the page and let it accumulate frame
// deltas into window.__frameDeltas until stopped. The longtask
// observer also writes into window.__longtasks. Both are read at the
// end via page.evaluate. The instrument intentionally lives on the
// page rather than the test harness so the timing reflects what the
// browser actually rendered, not Playwright's IPC round-trips.
async function installFrameInstrument(page: Page): Promise<void> {
  await page.evaluate(() => {
    const w = window as unknown as {
      __frameDeltas?: number[];
      __longtasks?: number;
      __frameRafId?: number;
      __frameLastT?: number;
      __longtaskObserver?: PerformanceObserver;
    };
    w.__frameDeltas = [];
    w.__longtasks = 0;
    const tick = (t: number) => {
      const last = w.__frameLastT;
      if (last !== undefined) w.__frameDeltas!.push(t - last);
      w.__frameLastT = t;
      w.__frameRafId = requestAnimationFrame(tick);
    };
    w.__frameRafId = requestAnimationFrame(tick);
    try {
      const obs = new PerformanceObserver((list) => {
        w.__longtasks = (w.__longtasks ?? 0) + list.getEntries().length;
      });
      obs.observe({ entryTypes: ["longtask"] });
      w.__longtaskObserver = obs;
    } catch {
      // longtask API isn't available in headless contexts < some
      // versions; observer absence just means the secondary signal
      // is missing, not a failure.
    }
  });
}

async function readFrameInstrument(page: Page): Promise<FrameStats> {
  return page.evaluate(() => {
    const w = window as unknown as {
      __frameDeltas?: number[];
      __longtasks?: number;
      __frameRafId?: number;
      __longtaskObserver?: PerformanceObserver;
    };
    if (w.__frameRafId !== undefined) cancelAnimationFrame(w.__frameRafId);
    if (w.__longtaskObserver) w.__longtaskObserver.disconnect();
    const deltas = (w.__frameDeltas ?? []).slice().sort((a, b) => a - b);
    const median =
      deltas.length === 0 ? 0 : (deltas[Math.floor(deltas.length / 2)] ?? 0);
    const p95 =
      deltas.length === 0
        ? 0
        : (deltas[Math.floor(deltas.length * 0.95)] ?? deltas[deltas.length - 1] ?? 0);
    const max = deltas.length === 0 ? 0 : (deltas[deltas.length - 1] ?? 0);
    const sum = deltas.reduce((a, b) => a + b, 0);
    return {
      frameCount: deltas.length,
      durationMs: sum,
      medianFrameMs: median,
      p95FrameMs: p95,
      maxFrameMs: max,
      longtaskCount: w.__longtasks ?? 0,
    };
  });
}

async function runScaleScenario(
  page: Page,
  variant: string,
  injectStyle?: string,
): Promise<ScaleResult> {
  let apiMediaCount = 0;
  let thumbCount = 0;
  page.on("request", (req: Request) => {
    const path = new URL(req.url()).pathname;
    if (path === "/api/v1/media") apiMediaCount += 1;
    if (path.endsWith("/thumb") && path.startsWith("/api/v1/media/")) {
      thumbCount += 1;
    }
  });

  const cdp = await page.context().newCDPSession(page);

  // Initial paint. waitForResponse on the first /api/v1/media call so
  // captures run against a steady-state grid, not a half-rendered one.
  await Promise.all([
    page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === "/api/v1/media" && r.status() === 200,
    ),
    page.goto("/"),
  ]);
  if (injectStyle) {
    // Injected after initial paint so the page boots normally; the
    // override only affects the scroll-loop measurement window.
    await page.addStyleTag({ content: injectStyle });
  }
  await expect(
    page.locator("[data-media-id]").first(),
  ).toBeVisible({ timeout: 10_000 });

  const initial = await captureSample(
    page,
    cdp,
    0,
    () => apiMediaCount,
    () => thumbCount,
  );

  // Start the rAF + longtask instrument right before the scroll loop
  // so the frame-deltas reflect scroll cost, not initial-paint cost.
  await installFrameInstrument(page);

  // Scroll-to-bottom 10 times. Each iteration: scroll to scrollHeight,
  // wait for the next /api/v1/media response, capture per-scroll
  // sample. mediaStore exhaustion (next_offset:null) would cause the
  // wait to time out — 10 iterations stay well under the 100k-row /
  // 200-per-page = 500-page budget.
  const PAGES = 10;
  const perScroll: PerScrollSample[] = [];
  for (let i = 0; i < PAGES; i++) {
    await Promise.all([
      page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname === "/api/v1/media" && r.status() === 200,
        { timeout: 30_000 },
      ),
      page.evaluate(() => {
        const main = document.querySelector(".main");
        if (main) main.scrollTo(0, main.scrollHeight);
      }),
    ]);
    perScroll.push(
      await captureSample(
        page,
        cdp,
        i + 1,
        () => apiMediaCount,
        () => thumbCount,
      ),
    );
  }

  const scrollFrames = await readFrameInstrument(page);
  await cdp.detach();

  return {
    variant,
    initial,
    perScroll,
    scrollFrames,
    pagesScrolled: PAGES,
  };
}

async function persistResult(result: ScaleResult): Promise<void> {
  const fs = await import("node:fs/promises");
  const path = await import("node:path");
  const dir = path.resolve(".snapshots");
  await fs.mkdir(dir, { recursive: true });
  const stamp = new Date().toISOString().replace(/[:.]/g, "-");
  const safeVariant = result.variant.replace(/[^a-z0-9-]/gi, "_");
  await fs.writeFile(
    path.join(dir, `library-${safeVariant}-${stamp}.json`),
    JSON.stringify(result, null, 2),
  );
}

test.describe("/library scale (100k rows, no thumb files)", () => {
  test("baseline: per-scroll samples + rAF frame intervals (c-v applied)", async ({
    page,
  }) => {
    const result = await runScaleScenario(page, "cv-applied");
    console.log("[scale/library cv-applied] INITIAL", JSON.stringify(result.initial));
    for (const s of result.perScroll) {
      console.log("[scale/library cv-applied] SCROLL", JSON.stringify(s));
    }
    console.log(
      "[scale/library cv-applied] FRAMES",
      JSON.stringify(result.scrollFrames),
    );

    const last = result.perScroll[result.perScroll.length - 1];
    expect(last).toBeDefined();
    if (last) {
      // Soft floors — sized to fail on 5-10x regression, not tight.
      expect(last.domNodeCount).toBeLessThan(50_000);
      expect(last.apiMediaRequests).toBeLessThan(50);
      expect(last.thumbRequests).toBeLessThan(5_000);
    }

    await persistResult(result);
  });

  test("ablated: same but with content-visibility disabled via injected CSS", async ({
    page,
  }) => {
    // The override targets the two selectors that carry
    // content-visibility:auto: the chunk wrapper (.month) and the cell
    // wrapper (.cell). Anything else with c-v is unrelated.
    const override = `.month, .cell { content-visibility: visible !important; contain-intrinsic-size: auto !important; }`;
    const result = await runScaleScenario(page, "cv-disabled", override);
    console.log("[scale/library cv-disabled] INITIAL", JSON.stringify(result.initial));
    for (const s of result.perScroll) {
      console.log("[scale/library cv-disabled] SCROLL", JSON.stringify(s));
    }
    console.log(
      "[scale/library cv-disabled] FRAMES",
      JSON.stringify(result.scrollFrames),
    );

    // Same soft floors apply — a regression that breaks the grid
    // would fail both variants identically.
    const last = result.perScroll[result.perScroll.length - 1];
    expect(last).toBeDefined();
    if (last) {
      expect(last.domNodeCount).toBeLessThan(50_000);
      expect(last.apiMediaRequests).toBeLessThan(50);
      expect(last.thumbRequests).toBeLessThan(5_000);
    }

    await persistResult(result);
  });
});
