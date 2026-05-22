// frontend/src/lib/lightbox/lightboxSession.svelte.ts
//
// App-level snapshot of the source state at lightbox-open time. Source
// routes capture this BEFORE navigation; the lightbox reads it from
// any route. Route-local stores (AlbumDetailStore, HiddenMediaStore)
// are unmounted during lightbox display, so the snapshot is the only
// durable handoff between source and lightbox.

import type { SearchScoreComponents } from "../search/types";

export type LightboxSource =
  | { kind: "library" }
  | { kind: "sessions" }
  | { kind: "album"; albumId: string }
  | { kind: "hidden" }
  | { kind: "search" }
  | { kind: "map" };

// scoreComponentsById is the per-media diagnostics payload threaded
// from the Search route into the lightbox. Only the search source
// populates it; other source kinds leave it undefined and the
// lightbox surfaces no relevance row. The map is keyed by media_id —
// values come from SearchResult.score_components on the wire and are
// only present when the request was issued with explain=true AND the
// AI Inspection toggle was on. Hits without score_components are
// simply absent from the map; the lookup miss renders no row.
// qHash binds a search snapshot to the canonical key of the search
// state (q + filters + sort) it was captured under. The Search route
// computes the hash, sets it on the snapshot, AND mirrors it onto the
// /media/:id URL via ?qhash=...; the lightbox compares the URL value
// against the snapshot's qHash before trusting navIds /
// scoreComponentsById. Without this binding a stale search snapshot
// from an earlier query could be served for a fresh direct entry to
// /media/:id?from=search (shared URL, browser back, etc.), yielding
// wrong prev/next and stale relevance diagnostics. Only the search
// source populates this field; other source kinds leave it undefined
// and the lightbox skips the hash check (those sources reconstruct
// from the URL alone, so a stale snapshot is harmless there because
// the activeId-in-navIds + from-kind agreement check already covers
// it for the same source).
export type LightboxSnapshot = {
  source: LightboxSource;
  navIds: string[];
  selected: boolean;
  scrollY: number;
  returnFocusMediaId: string | null;
  returnHref: string;
  // includeHidden is set when navIds came from a hidden-aware fetch
  // (currently only produced by the map page when the user has the
  // Include-hidden toggle on AND has a valid unlock claim). Other
  // sources leave it undefined/false. The lightbox uses it (in E2) to
  // avoid treating a hidden row in a snapshot as a cross-context leak
  // when the snapshot was fetched under explicit include_hidden=true.
  includeHidden?: boolean;
  scoreComponentsById?: Map<string, SearchScoreComponents>;
  qHash?: string;
};

export class LightboxSessionStore {
  snapshot = $state<LightboxSnapshot | null>(null);

  open(s: LightboxSnapshot): void {
    // Defensive copy: callers (source routes) build the snapshot from
    // their own derived state and may continue to mutate that array
    // after handoff (e.g. infinite-scroll appends, hide/unhide).
    // Capture by value so the snapshot is a true point-in-time
    // record, immune to source-side mutation.
    //
    // scoreComponentsById is similarly cloned so a re-fetch in the
    // search store (which atomically replaces the map) doesn't mutate
    // the snapshot's view. Values inside the map (the score component
    // records) are immutable plain objects on the wire, so a shallow
    // Map clone is sufficient.
    const next: LightboxSnapshot = {
      ...s,
      navIds: [...s.navIds],
      ...(s.scoreComponentsById !== undefined
        ? { scoreComponentsById: new Map(s.scoreComponentsById) }
        : {}),
    };
    this.snapshot = next;
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
