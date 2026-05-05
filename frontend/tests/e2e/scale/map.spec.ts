import { test, expect, type Page } from "@playwright/test";

// Scale spec: /map at 100k rows (~30k geotagged, ~12k biased into a
// 2°×2° SF Bay Area hot zone, the rest spread globally — see
// internal/testutil/mediaseed/scale.go's HotZoneFraction). Boots the
// same e2e-server as library.spec.ts via playwright-e2e-scale.config.ts.
//
// What this measures:
//   - Initial render at continent zoom (z=4) centered on the hot zone.
//     The hot-zone markers collapse into a few large clusters; the
//     globally-spread markers contribute a couple hundred more
//     scattered clusters. Marker/cluster DOM count, listener count,
//     JS errors all sampled here.
//   - Click-zoom from 4 → 10: each step splits the hot-zone clusters
//     deeper through markercluster's disclosure tree, which is the
//     cost-heavy path (markercluster recomputes child sets, Leaflet
//     re-renders the marker pane, and our DivIcon HTML is rebuilt for
//     every cluster).
//   - rAF frame intervals + longtask count across the whole zoom loop.
//
// PS-6 (kata #25) tightened the count-based floors from "catastrophic
// 5-10x ceiling" to "2x of observed" floors after collecting spread
// across 3 successive runs (see assertScaleFloors below). The
// page-error assertion is hard — there's no flake budget for "the map
// crashed under load".

interface DOMCounters {
  documents: number;
  nodes: number;
  jsEventListeners: number;
}

interface MapSample {
  iter: number;
  zoom: number;
  markerIconCount: number;
  clusterIconCount: number;
  totalIconCount: number;
  domNodeCount: number;
  cdpNodes: number;
  cdpJSEventListeners: number;
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
  initial: MapSample;
  perZoom: MapSample[];
  zoomFrames: FrameStats;
  pageErrors: string[];
}

async function captureMapSample(
  page: Page,
  cdp: import("@playwright/test").CDPSession,
  iter: number,
): Promise<MapSample> {
  const counts = await page.evaluate(() => ({
    // Leaflet appends BOTH photo markers and clusters to
    // .leaflet-marker-pane with class .leaflet-marker-icon — DivIcon
    // and image-marker chrome alike. We split them via the icon's
    // className: MapPane's photoMarkerIcon uses "map-photo-pin-wrap"
    // and clusterIconHtml uses "map-cluster-pin-wrap". markercluster's
    // default ".marker-cluster" class is NOT present because our
    // iconCreateFunction replaces the className. Counting both
    // sub-classes separately splits photos from clusters without
    // needing the leaflet.markercluster default chrome.
    markerIconCount: document.querySelectorAll(".map-photo-pin-wrap").length,
    clusterIconCount: document.querySelectorAll(".map-cluster-pin-wrap").length,
    totalIconCount: document.querySelectorAll(".leaflet-marker-icon").length,
    domNodeCount: document.querySelectorAll("*").length,
    zoom: ((): number => {
      // The Leaflet zoom level shows up on .leaflet-pane via no public
      // attribute, so read the current zoom from the URL query string
      // instead — Map.svelte writes ?z= on every moveend/zoomend (300ms
      // debounce), and we wait for that write below before capturing.
      const sp = new URLSearchParams(window.location.search);
      const z = sp.get("z");
      return z !== null ? Number(z) : NaN;
    })(),
  }));
  const cdpCounters = (await cdp.send("Memory.getDOMCounters")) as DOMCounters;
  return {
    iter,
    zoom: counts.zoom,
    markerIconCount: counts.markerIconCount,
    clusterIconCount: counts.clusterIconCount,
    totalIconCount: counts.totalIconCount,
    domNodeCount: counts.domNodeCount,
    cdpNodes: cdpCounters.nodes,
    cdpJSEventListeners: cdpCounters.jsEventListeners,
  };
}

// installFrameInstrument / readFrameInstrument mirror the helpers in
// library.spec.ts. They're duplicated rather than extracted into a
// shared module because the two specs are the only callers and the
// instrument is small enough that drift risk is low; if a third
// caller appears, hoist them.
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
      // longtask API is missing on some headless builds; observer
      // absence reduces this signal to "deltas only" and isn't a failure.
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

// waitForZoomSettled waits for the Leaflet zoom animation to complete
// AND for Map.svelte's debounced URL writer to commit ?z=. We can't
// observe Leaflet's zoomend directly without exposing the map
// instance, so we poll the URL — Map.svelte writes to history.replaceState
// 300ms after the last moveend, which fires on zoomend. The poll runs
// inside page.waitForFunction so it doesn't block the rAF instrument
// from sampling frame intervals during the wait.
async function waitForZoomSettled(page: Page, expectedZoom: number): Promise<void> {
  await page.waitForFunction(
    (z: number) => {
      const sp = new URLSearchParams(window.location.search);
      return sp.get("z") === String(z);
    },
    expectedZoom,
    { timeout: 5_000 },
  );
}

async function persistResult(result: ScaleResult): Promise<void> {
  const fs = await import("node:fs/promises");
  const path = await import("node:path");
  const dir = path.resolve(".snapshots");
  await fs.mkdir(dir, { recursive: true });
  const stamp = new Date().toISOString().replace(/[:.]/g, "-");
  const safeVariant = result.variant.replace(/[^a-z0-9-]/gi, "_");
  await fs.writeFile(
    path.join(dir, `map-${safeVariant}-${stamp}.json`),
    JSON.stringify(result, null, 2),
  );
}

async function logResult(result: ScaleResult): Promise<void> {
  console.log(`[scale/map ${result.variant}] INITIAL`, JSON.stringify(result.initial));
  for (const s of result.perZoom) {
    console.log(`[scale/map ${result.variant}] ZOOM`, JSON.stringify(s));
  }
  console.log(
    `[scale/map ${result.variant}] FRAMES`,
    JSON.stringify(result.zoomFrames),
  );
  if (result.pageErrors.length > 0) {
    console.log(
      `[scale/map ${result.variant}] PAGE_ERRORS`,
      JSON.stringify(result.pageErrors),
    );
  }
  await persistResult(result);
}

test.describe("/map scale (100k rows, ~12k hot-zone markers)", () => {
  test("zoom-in from continent to street level", async ({ page }) => {
    const pageErrors: string[] = [];
    page.on("pageerror", (err) => {
      pageErrors.push(err.message);
    });

    // Start at zoom 4 centered on the SF Bay Area hot zone — at this
    // zoom every hot-zone marker collapses into a single mega-cluster.
    // Stepping down to z=10 fans them through markercluster's
    // disclosure tree, which is the layout-heavy path this spec
    // measures.
    await Promise.all([
      page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname === "/api/v1/media/geo" && r.status() === 200,
        { timeout: 30_000 },
      ),
      page.goto("/map?z=4&c=37.5,-122.0"),
    ]);

    // map-loaded only renders once geoStore.ready && items.length > 0,
    // so awaiting it is a stronger signal than waiting on map-pane
    // alone (map-pane mounts in the loading state too).
    await expect(page.getByTestId("map-pane")).toBeVisible();
    await expect(page.getByTestId("map-loaded")).toBeVisible();
    await expect(page.locator(".leaflet-marker-pane")).toBeAttached();

    // Wait one rAF tick so the cluster icons rendered on the initial
    // moveend are committed before we sample. Without this the marker
    // count is read against an empty marker pane on fast machines.
    await page.evaluate(
      () => new Promise<void>((res) => requestAnimationFrame(() => res())),
    );

    const cdp = await page.context().newCDPSession(page);
    const initial = await captureMapSample(page, cdp, 0);

    // Sanity check: the hot-zone seed should produce SOME icon DOM at
    // z=4. totalIconCount covers both photo markers and clusters
    // since the hot zone might collapse to a single mega-cluster (in
    // which case markerIconCount alone is zero). A regression that
    // empties the geo set or breaks marker rendering would otherwise
    // pass the rest of the loop with zero counts and nothing to fail
    // on.
    expect(initial.totalIconCount).toBeGreaterThan(0);

    await installFrameInstrument(page);

    // Drive the zoom button six times: 4 → 5 → 6 → 7 → 8 → 9 → 10.
    // Stop at 10: at street level the hot-zone markers spread out
    // enough that individual photo pins dominate over clusters, which
    // is the steady-state shape we want to measure DOM ceilings
    // against. Going past 10 would just exercise tile loading at
    // diminishing cluster cost.
    const ZOOM_STEPS = 6;
    const startZoom = 4;
    const perZoom: MapSample[] = [];
    const zoomIn = page.locator(".leaflet-control-zoom-in");
    for (let i = 0; i < ZOOM_STEPS; i++) {
      await zoomIn.click();
      const expectedZoom = startZoom + i + 1;
      await waitForZoomSettled(page, expectedZoom);
      // Cluster recompute happens synchronously on zoomend but the
      // DivIcon HTML is rebuilt during the next paint — wait an rAF
      // tick so captureMapSample reads the post-paint marker pane.
      await page.evaluate(
        () => new Promise<void>((res) => requestAnimationFrame(() => res())),
      );
      perZoom.push(await captureMapSample(page, cdp, i + 1));
    }

    const zoomFrames = await readFrameInstrument(page);
    await cdp.detach();

    const result: ScaleResult = {
      variant: "zoom-in-hot-zone",
      initial,
      perZoom,
      zoomFrames,
      pageErrors,
    };
    await logResult(result);

    // Hard assertion: the map MUST NOT throw during the zoom loop.
    // Cluster recompute on a 12k-marker hot zone has plausible failure
    // modes (markercluster internals, our DivIcon HTML rebuild, the
    // viewport-change emitter) and a thrown exception there is a
    // user-visible regression we want to fail loudly on.
    expect(pageErrors).toEqual([]);

    // PS-6 hard floors (2026-05-05, commit 20171a1). Spread observed
    // across 3 successive runs (M5 Max, scaleRows=100_000) was
    // byte-identical for the count metrics — the seed is deterministic,
    // marker layout is deterministic, and the zoom button drives
    // Leaflet through the same code path every time:
    //
    //   sample        | totalIconCount | domNodeCount
    //   --------------+----------------+--------------
    //   initial (z=4) | 303            | 50269
    //   final  (z=10) | 235            | 8729
    //
    // initial.domNodeCount caps DOM growth at the most-clustered zoom
    // (every hot-zone marker plus all global markers folded into <300
    // cluster icons). last.domNodeCount caps it at the most-fanned-out
    // zoom (clusters split into individual photo pins, far fewer
    // overall). Both 2× observed.
    const FLOOR_DOM_INITIAL = 100_000; // 2× observed (50269)
    const FLOOR_DOM_LAST = 18_000; // 2× observed (8729)
    expect(initial.domNodeCount).toBeLessThan(FLOOR_DOM_INITIAL);
    const last = perZoom[perZoom.length - 1];
    expect(last).toBeDefined();
    if (last) {
      expect(last.domNodeCount).toBeLessThan(FLOOR_DOM_LAST);
      // Marker pane must still be attached and populated — a regression
      // that detaches the marker layer would zero this. totalIconCount
      // because at z=10 the viewport may still contain a mix of
      // photos and clusters depending on local density.
      expect(last.totalIconCount).toBeGreaterThan(0);
    }
  });
});
