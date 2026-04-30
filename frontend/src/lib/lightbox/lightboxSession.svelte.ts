// frontend/src/lib/lightbox/lightboxSession.svelte.ts
//
// App-level snapshot of the source state at lightbox-open time. Source
// routes capture this BEFORE navigation; the lightbox reads it from
// any route. Route-local stores (AlbumDetailStore, HiddenMediaStore)
// are unmounted during lightbox display, so the snapshot is the only
// durable handoff between source and lightbox.

export type LightboxSource =
  | { kind: "library" }
  | { kind: "sessions" }
  | { kind: "album"; albumId: string }
  | { kind: "hidden" };

export type LightboxSnapshot = {
  source: LightboxSource;
  navIds: string[];
  selected: boolean;
  scrollY: number;
  returnFocusMediaId: string | null;
  returnHref: string;
};

export class LightboxSessionStore {
  snapshot = $state<LightboxSnapshot | null>(null);

  open(s: LightboxSnapshot): void {
    // Defensive copy: callers (source routes) build the snapshot from
    // their own derived state and may continue to mutate that array
    // after handoff (e.g. infinite-scroll appends, hide/unhide).
    // Capture by value so the snapshot is a true point-in-time
    // record, immune to source-side mutation.
    this.snapshot = { ...s, navIds: [...s.navIds] };
  }

  close(): void {
    this.snapshot = null;
  }

  clearScroll(): void {
    if (this.snapshot === null) return;
    this.snapshot = { ...this.snapshot, scrollY: 0 };
  }

  clearReturnFocus(): void {
    if (this.snapshot === null) return;
    this.snapshot = { ...this.snapshot, returnFocusMediaId: null };
  }

  removeIds(ids: string[]): void {
    const cur = this.snapshot;
    if (cur === null || ids.length === 0) return;
    const drop = new Set(ids);
    const next = cur.navIds.filter((x) => !drop.has(x));
    if (next.length === cur.navIds.length) return;
    this.snapshot = { ...cur, navIds: next };
  }
}

export const lightboxSession = new LightboxSessionStore();
