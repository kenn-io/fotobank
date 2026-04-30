// Progressive image loader for the lightbox. Sequence:
//   1. paint cached grid URL if available (synchronous src assignment)
//   2. load + decode preview; swap when ready
//   3. load + decode large in background; swap when ready
// Prefetch immediate prev/next: preview first, then large; max 2 in
// flight. Cancellation is logical: each load() invocation increments
// the generation token; completions whose token doesn't match are
// dropped (decoded image discarded, references released for GC).

export type ThumbSize = "grid" | "preview" | "large";

export function thumbUrl(id: string, size: ThumbSize, version: number): string {
  // encodeURIComponent so ids that happen to contain reserved URL
  // characters (`/`, `?`, `#`) don't break the path or sneak extra
  // query params into the request.
  return `/api/v1/media/${encodeURIComponent(id)}/thumb?size=${size}&v=${version}`;
}

export type LoadOpts = {
  activeId: string;
  gridSrc: string | null; // already-cached grid url, or null
  previewUrl: string;
  largeUrl: string;
  onSrc: (url: string) => void; // called as the visible src changes
};

export type PrefetchOpts = {
  prevPreviewUrl: string | null;
  nextPreviewUrl: string | null;
  prevLargeUrl: string | null;
  nextLargeUrl: string | null;
};

type Fetcher = (url: string) => Promise<void>;

export class LightboxLoader {
  private generation = 0;
  private fetcher: Fetcher = defaultFetcher;
  private inFlight = 0;
  private queue: string[] = [];
  private static readonly MAX_INFLIGHT = 2;

  /** Override the fetcher (test injection). */
  setFetcher(f: Fetcher): void {
    this.fetcher = f;
  }

  cancelAll(): void {
    this.generation += 1;
    this.queue = [];
  }

  async load(opts: LoadOpts): Promise<void> {
    this.generation += 1;
    const myGen = this.generation;
    const { gridSrc, previewUrl, largeUrl, onSrc } = opts;

    if (gridSrc !== null) onSrc(gridSrc);

    try {
      await loadAndDecode(previewUrl);
    } catch {
      return; // preview failed; keep prior src visible
    }
    if (myGen !== this.generation) return;
    onSrc(previewUrl);

    try {
      await loadAndDecode(largeUrl);
    } catch {
      return;
    }
    if (myGen !== this.generation) return;
    onSrc(largeUrl);
  }

  prefetch(opts: PrefetchOpts): void {
    const previews = [opts.prevPreviewUrl, opts.nextPreviewUrl].filter(notNull);
    const larges = [opts.prevLargeUrl, opts.nextLargeUrl].filter(notNull);
    this.queue = [...previews, ...larges];
    this.pump();
  }

  private pump(): void {
    while (this.inFlight < LightboxLoader.MAX_INFLIGHT && this.queue.length > 0) {
      const url = this.queue.shift();
      if (url === undefined) return;
      this.inFlight += 1;
      this.fetcher(url)
        .catch(() => undefined)
        .finally(() => {
          this.inFlight -= 1;
          this.pump();
        });
    }
  }
}

function notNull<T>(x: T | null): x is T {
  return x !== null;
}

async function loadAndDecode(url: string): Promise<void> {
  const img = new Image();
  if (typeof img.decode === "function") {
    img.src = url;
    await img.decode();
    return;
  }
  // Install handlers BEFORE assigning src — a cached or
  // synchronously-completing image fires load/error during the
  // src= assignment, and any handler attached later would miss
  // the event and leave this Promise pending forever.
  await new Promise<void>((resolve, reject) => {
    img.onload = (): void => resolve();
    img.onerror = (): void => reject(new Error("image load failed"));
    img.src = url;
  });
}

function defaultFetcher(url: string): Promise<void> {
  // Image() preload has the simplest browser semantics for cache
  // population. Result is dropped — we only care about the side
  // effect of warming HTTP cache.
  return new Promise<void>((resolve, reject) => {
    const img = new Image();
    img.onload = (): void => resolve();
    img.onerror = (): void => reject(new Error("prefetch failed"));
    img.src = url;
  });
}
