// frontend/src/lib/lightbox/scrollRestore.svelte.ts
//
// Guarded scroll restoration helper. Source routes (Library, Sessions,
// AlbumDetail, HiddenLibrary) reload after lightbox close. The document
// scrollHeight is typically 0 at remount; scrolling immediately would
// clamp to top. markPending() stores the saved Y and target media id;
// attemptRestore() retries on each load completion until either the
// target element exists or scrollHeight is large enough, capped at
// maxAttempts (~10) to handle infinite-scroll edge cases.

export type Pending = {
  scrollY: number;
  mediaId: string | null;
};

export type ScrollRestoreOptions = {
  maxAttempts?: number; // default 10
};

export class ScrollRestore {
  private pending: Pending | null = null;
  private attempts = 0;
  private readonly maxAttempts: number;

  constructor(opts: ScrollRestoreOptions = {}) {
    this.maxAttempts = opts.maxAttempts ?? 10;
  }

  markPending(p: Pending): void {
    this.pending = p;
    this.attempts = 0;
  }

  isPending(): boolean {
    return this.pending !== null;
  }

  /**
   * Try to restore scroll. Returns true if restoration ran (either
   * because conditions were met or because the cap was hit and we
   * settled for a partial restore). Once true, no further calls
   * scroll until the next markPending().
   */
  attemptRestore(): boolean {
    const p = this.pending;
    if (p === null) return false;
    this.attempts += 1;

    const targetEl = p.mediaId !== null
      ? document.querySelector(`[data-media-id="${cssEscape(p.mediaId)}"]`)
      : null;
    const enoughContent = document.body.scrollHeight >= p.scrollY + window.innerHeight;
    const capHit = this.attempts > this.maxAttempts;

    if (targetEl !== null || enoughContent) {
      window.scrollTo(0, p.scrollY);
      this.pending = null;
      return true;
    }
    if (capHit) {
      const partial = Math.max(0, document.body.scrollHeight - window.innerHeight);
      window.scrollTo(0, Math.min(p.scrollY, partial));
      this.pending = null;
      return true;
    }
    return false;
  }
}

// CSS.escape may be missing in older test environments; provide a
// minimal fallback that handles ids commonly produced by the backend
// (UUIDs, lowercase alnum + hyphens). Real ids never contain quotes
// or backslashes, but escape defensively for the CSS selector.
function cssEscape(s: string): string {
  if (typeof CSS !== "undefined" && typeof CSS.escape === "function") return CSS.escape(s);
  return s.replace(/["\\]/g, "\\$&");
}
