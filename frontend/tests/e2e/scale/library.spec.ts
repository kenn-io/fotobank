import { test, expect, type Page, type Request } from "@playwright/test";

// Scale spec: /library at 100k rows. The e2e-server is booted with
// FOTOBANK_E2E_SCALE_ROWS=100000 (see playwright-e2e-scale.config.ts),
// which replaces the curated fixtures with a bulk SeedScaleLibrary
// call.
//
// Thumb files are off by default — /thumb 404s for every cell, the
// SPA renders the broken-image placeholder, and the request count
// this spec measures includes those 404s. Set
// FOTOBANK_E2E_SCALE_REAL_THUMBS=1 to seed a real (tiny) grid-tier
// JPEG per row; PS-3f's content-visibility re-evaluation depends on
// this so img.decode work is actually present to defer.
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
// - A/B variant: cell-level content-visibility:auto was REMOVED from
//   source in PS-3e; PS-3f re-injects it via page.addStyleTag in the
//   "cv-applied" variants and runs against real thumbs. Compare frame
//   intervals across the two variants to decide whether to re-add c-v
//   to MonthChunk. With no real thumbs the inject becomes a no-op as
//   far as decode-deferral goes — only c-v's bookkeeping cost surfaces.
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
  thumb200: number;
  thumb404: number;
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
  thumb200Count: () => number,
  thumb404Count: () => number,
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
    thumb200: thumb200Count(),
    thumb404: thumb404Count(),
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

// scrollMode picks the seek pattern for the scroll loop:
//  - "bottom": scrollTo(scrollHeight) — rapid-fire, the worst case for
//    content-visibility:auto's deferred layout (PS-3c finding).
//  - "dwell":  scroll one viewport at a time with a 100ms inter-step
//    delay simulating user dwell. PS-3e hypothesis: c-v's deferred
//    layout amortizes over per-step commits and is benign or
//    beneficial here, unlike under "bottom".
type ScrollMode = "bottom" | "dwell";

async function runScaleScenario(
  page: Page,
  variant: string,
  scrollMode: ScrollMode,
  injectStyle?: string,
): Promise<ScaleResult> {
  let apiMediaCount = 0;
  let thumbCount = 0;
  let thumb200Count = 0;
  let thumb404Count = 0;
  page.on("request", (req: Request) => {
    const path = new URL(req.url()).pathname;
    if (path === "/api/v1/media") apiMediaCount += 1;
    if (path.endsWith("/thumb") && path.startsWith("/api/v1/media/")) {
      thumbCount += 1;
    }
  });
  // Status counts come from the response event so we can split
  // between served bytes (200, real-thumbs mode) and placeholders
  // (404, default mode). The split is the only way to verify
  // FOTOBANK_E2E_SCALE_REAL_THUMBS=1 actually wrote blobs — request
  // counts alone don't distinguish the two modes.
  page.on("response", (resp) => {
    const path = new URL(resp.url()).pathname;
    if (path.endsWith("/thumb") && path.startsWith("/api/v1/media/")) {
      if (resp.status() === 200) thumb200Count += 1;
      else if (resp.status() === 404) thumb404Count += 1;
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
    () => thumb200Count,
    () => thumb404Count,
  );

  // Start the rAF + longtask instrument right before the scroll loop
  // so the frame-deltas reflect scroll cost, not initial-paint cost.
  await installFrameInstrument(page);

  // Drive PAGES /api/v1/media fetches via the chosen scroll pattern.
  //
  // bottom mode: scrollTo(scrollHeight) on each iteration; one fetch
  //   reliably trips per scroll because the bottom sentinel jumps
  //   straight into the IO buffer.
  // dwell mode: scroll one viewport at a time with a 100ms delay,
  //   simulating user dwell. The IO sentinel needs multiple
  //   viewport-height steps to come into range, so the loop scrolls
  //   until /api/v1/media fires (or we hit a step ceiling). The
  //   scroll-step count is variable, but the fetched-page count is
  //   pinned at PAGES so the per-scroll sample series is comparable.
  const PAGES = 10;
  const perScroll: PerScrollSample[] = [];
  for (let i = 0; i < PAGES; i++) {
    if (scrollMode === "bottom") {
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
    } else {
      // dwell mode: step by one viewport, wait 100ms, repeat. Stop
      // when /api/v1/media fires for this iteration. A 30-step ceiling
      // protects against an infinite loop if the sentinel never trips.
      const responsePromise = page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname === "/api/v1/media" && r.status() === 200,
        { timeout: 30_000 },
      );
      let stepped = 0;
      const MAX_STEPS = 30;
      while (stepped < MAX_STEPS) {
        const advanced = await page.evaluate(() => {
          const main = document.querySelector(".main") as HTMLElement | null;
          if (!main) return false;
          const before = main.scrollTop;
          main.scrollBy(0, main.clientHeight);
          // scrollBy is async on some Chrome versions; force a layout
          // read so the next .scrollTop check sees the updated value.
          return main.scrollTop > before;
        });
        if (!advanced) break;
        stepped += 1;
        // Race: poll for the response or sleep 100ms and continue.
        const settled = await Promise.race([
          responsePromise.then(() => true),
          new Promise<false>((res) => setTimeout(() => res(false), 100)),
        ]);
        if (settled) break;
      }
      await responsePromise;
    }
    // waitForResponse resolves when bytes arrive, not when Svelte has
    // committed the new state to the DOM. Wait one rAF tick so the
    // captureSample below reads counts that match the freshly-rendered
    // page rather than the previous frame's state.
    await page.evaluate(
      () => new Promise<void>((res) => requestAnimationFrame(() => res())),
    );
    perScroll.push(
      await captureSample(
        page,
        cdp,
        i + 1,
        () => apiMediaCount,
        () => thumbCount,
        () => thumb200Count,
        () => thumb404Count,
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

// CV_INJECT applies cell-level content-visibility:auto via
// page.addStyleTag after initial paint. The "cv-applied" variants
// inject this; the "cv-disabled" variants run with source defaults
// (no c-v on cells). PS-3e removed cell-c-v from source after the
// no-real-images A/B showed it costs without measurable benefit;
// PS-3f re-runs the A/B with real thumb files seeded so img.decode
// work is actually present to defer.
//
// contain-intrinsic-size uses `auto 200px` so the placeholder size
// is the last-rendered size when known and falls back to 200px on
// first paint — the .cell wrapper has explicit width/height inline,
// so this only matters when c-v skips layout entirely for offscreen
// cells.
const CV_INJECT = `.cell { content-visibility: auto; contain-intrinsic-size: auto 200px; }`;

async function logResult(result: ScaleResult): Promise<void> {
  console.log(`[scale/library ${result.variant}] INITIAL`, JSON.stringify(result.initial));
  for (const s of result.perScroll) {
    console.log(`[scale/library ${result.variant}] SCROLL`, JSON.stringify(s));
  }
  console.log(
    `[scale/library ${result.variant}] FRAMES`,
    JSON.stringify(result.scrollFrames),
  );
  await persistResult(result);
}

// realThumbsMode mirrors playwright-e2e-scale.config.ts's resolution:
// FOTOBANK_E2E_SCALE_REAL_THUMBS=1|true means the e2e-server seeded
// real grid JPEGs, so /thumb returns 200. Anything else means default
// mode where /thumb 404s for every cell. The spec uses this to assert
// that thumb status codes match the server's seeding mode — without
// that assertion, a future regression that silently drops the
// real-thumbs seed (or a future regression in the /thumb handler that
// 404s every request) would leave PS-3f's content-visibility A/B
// running against a degenerate fixture without any test failure.
const realThumbsMode = ((): boolean => {
  const v = process.env["FOTOBANK_E2E_SCALE_REAL_THUMBS"] ?? "";
  return v === "1" || v === "true";
})();

function assertSoftFloors(result: ScaleResult): void {
  const last = result.perScroll[result.perScroll.length - 1];
  expect(last).toBeDefined();
  if (last) {
    // Soft floors — sized to fail on 5-10x regression, not tight.
    expect(last.domNodeCount).toBeLessThan(50_000);
    expect(last.apiMediaRequests).toBeLessThan(50);
    expect(last.thumbRequests).toBeLessThan(5_000);

    // Mode-aware thumb status assertions: every test must see the
    // expected status mix. Real-thumbs mode requires at least one 200
    // and zero 404s; default mode requires the inverse. Both modes
    // demand SOME thumb traffic — a regression that suppresses thumb
    // requests entirely (e.g. broken thumbUrl construction) would
    // otherwise satisfy "thumb404 == 0" trivially in real-thumbs mode.
    if (realThumbsMode) {
      expect(last.thumb200).toBeGreaterThan(0);
      expect(last.thumb404).toBe(0);
    } else {
      expect(last.thumb404).toBeGreaterThan(0);
      expect(last.thumb200).toBe(0);
    }
  }
}

test.describe("/library scale (100k rows)", () => {
  test("scroll-to-bottom, c-v disabled", async ({ page }) => {
    // Source default: no cell-c-v in MonthChunk.svelte (PS-3e). The
    // baseline these "cv-disabled" variants establish is what ships
    // today — used as the comparand for the cv-injected variants.
    const result = await runScaleScenario(page, "cv-disabled-bottom", "bottom");
    await logResult(result);
    assertSoftFloors(result);
  });

  test("scroll-to-bottom, c-v applied", async ({ page }) => {
    const result = await runScaleScenario(
      page,
      "cv-applied-bottom",
      "bottom",
      CV_INJECT,
    );
    await logResult(result);
    assertSoftFloors(result);
  });

  test("dwell-scroll, c-v disabled", async ({ page }) => {
    const result = await runScaleScenario(page, "cv-disabled-dwell", "dwell");
    await logResult(result);
    assertSoftFloors(result);
  });

  test("dwell-scroll, c-v applied", async ({ page }) => {
    // PS-3e hypothesis: c-v's per-step layout commit amortizes here,
    // unlike rapid scroll-to-bottom. PS-3f re-tests with real thumbs.
    const result = await runScaleScenario(
      page,
      "cv-applied-dwell",
      "dwell",
      CV_INJECT,
    );
    await logResult(result);
    assertSoftFloors(result);
  });
});
