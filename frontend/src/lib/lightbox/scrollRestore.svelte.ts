// frontend/src/lib/lightbox/scrollRestore.svelte.ts
//
// Guarded scroll restoration helper. Source routes (Library, Sessions,
// AlbumDetail, HiddenLibrary) reload after lightbox close. The container's
// scrollHeight is typically 0 at remount; scrolling immediately would
// clamp to top. markPending() stores the saved Y and target media id;
// attemptRestore() retries on each load completion until either the
// target element exists or scrollHeight is large enough, capped at
// maxAttempts (~10) to handle infinite-scroll edge cases.
//
// The scroll happens inside the ThreeColumnLayout `.main` element
// (overflow: auto) — NOT the document/window. Source routes capture
// `mainEl.scrollTop` at lightbox-open time; this helper restores into
// the same element. If the container is missing on a given attempt
// (route hasn't remounted yet), the call counts as a retry; if it
// never appears within the cap, the pending state is cleared.

export type Pending = {
  scrollY: number;
  mediaId: string | null;
};

export type ScrollRestoreOptions = {
  maxAttempts?: number; // default 10
  containerSelector?: string; // default ".main"
};

export class ScrollRestore {
  private pending: Pending | null = null;
  private attempts = 0;
  private readonly maxAttempts: number;
  private readonly containerSelector: string;

  constructor(opts: ScrollRestoreOptions = {}) {
    this.maxAttempts = opts.maxAttempts ?? 10;
    this.containerSelector = opts.containerSelector ?? ".main";
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

    const container = document.querySelector<HTMLElement>(this.containerSelector);
    // Source route hasn't remounted yet — count the call as a retry.
    // Keep pending so a later attempt can still succeed; bail only
    // when the cap is reached so the helper never gets stuck.
    if (container === null) {
      if (this.attempts >= this.maxAttempts) {
        this.pending = null;
        return true;
      }
      return false;
    }

    const targetEl = p.mediaId !== null
      ? container.querySelector(`[data-media-id="${cssEscape(p.mediaId)}"]`)
      : null;
    const enoughContent = container.scrollHeight >= p.scrollY + container.clientHeight;
    const capHit = this.attempts >= this.maxAttempts;

    if (targetEl !== null || enoughContent) {
      container.scrollTo(0, p.scrollY);
      this.pending = null;
      return true;
    }
    if (capHit) {
      const partial = Math.max(0, container.scrollHeight - container.clientHeight);
      container.scrollTo(0, Math.min(p.scrollY, partial));
      this.pending = null;
      return true;
    }
    return false;
  }
}

// Fallback only — modern browsers and jsdom both provide CSS.escape.
// Real ids never contain quotes or backslashes, but escape defensively
// for the CSS selector.
function cssEscape(s: string): string {
  if (typeof CSS !== "undefined" && typeof CSS.escape === "function") return CSS.escape(s);
  return s.replace(/["\\]/g, "\\$&");
}

/**
 * Read scrollTop from the source-route scroll container at lightbox-open
 * time. Centralizes the `.main` lookup so source routes (Library,
 * Sessions, AlbumDetail, HiddenLibrary) all capture the same Y that
 * ScrollRestore later reads back. Returns 0 when the document is
 * unavailable (SSR-style harnesses) or the container hasn't mounted.
 */
export function captureMainScrollY(selector = ".main"): number {
  if (typeof document === "undefined") return 0;
  const el = document.querySelector<HTMLElement>(selector);
  return el?.scrollTop ?? 0;
}
