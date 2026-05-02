// frontend/src/lib/router/router.svelte.ts

import type { SearchSort } from "../search/types";

export type RouteMatch =
  | { route: "library" }
  | { route: "sessions" }
  | { route: "settings" }
  | { route: "settings.ai" }
  | { route: "albums" }
  | { route: "albums.detail"; id: string }
  | { route: "shares"; album_id?: string; show_revoked?: boolean }
  | { route: "media"; id: string; from?: string }
  | { route: "hidden" }
  | {
      route: "search";
      q?: string;
      sort?: SearchSort;
      date_after?: string;
      date_before?: string;
      tag?: string[];
      location?: string;
      media_type?: "photo" | "video";
      include_hidden?: boolean;
    }
  | { route: "notfound"; path: string };

// Anchored patterns. Order doesn't matter — each regex tests in
// isolation; first match wins. Search and hash are stripped before
// matching so /media/abc?x=1 matches /media/:id.
const PATTERNS: Array<{ re: RegExp; build: (m: RegExpMatchArray) => RouteMatch }> = [
  { re: /^\/$/,           build: () => ({ route: "library" }) },
  { re: /^\/library$/,    build: () => ({ route: "library" }) },
  { re: /^\/sessions$/,   build: () => ({ route: "sessions" }) },
  { re: /^\/settings\/ai\/?$/, build: () => ({ route: "settings.ai" }) },
  { re: /^\/settings$/,   build: () => ({ route: "settings" }) },
  { re: /^\/albums\/?$/,  build: () => ({ route: "albums" }) },
  { re: /^\/albums\/([^/]+)\/?$/, build: (m) => ({ route: "albums.detail", id: m[1]! }) },
  // /shares accepts optional ?album_id and ?show_revoked query params.
  // With exactOptionalPropertyTypes, optional discriminant properties
  // can't be set to undefined — spread the keys only when present so
  // plain `/shares` produces exactly `{ route: "shares" }` (no extras).
  { re: /^\/shares\/?$/,  build: () => {
    const sp = new URLSearchParams(window.location.search);
    const albumId = sp.get("album_id");
    const showRevoked = sp.get("show_revoked") === "true";
    return {
      route: "shares" as const,
      ...(albumId ? { album_id: albumId } : {}),
      ...(showRevoked ? { show_revoked: true } : {}),
    };
  } },
  { re: /^\/media\/([^/]+)$/, build: (m) => {
    const sp = new URLSearchParams(window.location.search);
    const fromRaw = sp.get("from");
    const from = parseFrom(fromRaw);
    return {
      route: "media" as const,
      id: m[1]!,
      ...(from !== null ? { from } : {}),
    };
  } },
  { re: /^\/hidden\/?$/, build: () => ({ route: "hidden" as const }) },
  // /search accepts the full filter/sort surface as query params.
  // Unknown sort/media_type values are dropped (treated as absent) so
  // `?sort=garbage` doesn't poison the store; the page falls back to
  // its default. tag is repeated (`?tag=a&tag=b`) and only included
  // when at least one value is present.
  { re: /^\/search\/?$/, build: () => {
    const sp = new URLSearchParams(window.location.search);
    const q = sp.get("q");
    const sortRaw = sp.get("sort");
    const sort = parseSearchSort(sortRaw);
    const dateAfter = sp.get("date_after");
    const dateBefore = sp.get("date_before");
    const tags = sp.getAll("tag").filter((t) => t !== "");
    const location = sp.get("location");
    const mediaTypeRaw = sp.get("media_type");
    const mediaType = parseMediaType(mediaTypeRaw);
    const includeHidden = sp.get("include_hidden") === "true";
    return {
      route: "search" as const,
      ...(q != null && q !== "" ? { q } : {}),
      ...(sort !== null ? { sort } : {}),
      ...(dateAfter != null && dateAfter !== "" ? { date_after: dateAfter } : {}),
      ...(dateBefore != null && dateBefore !== "" ? { date_before: dateBefore } : {}),
      ...(tags.length > 0 ? { tag: tags } : {}),
      ...(location != null && location !== "" ? { location } : {}),
      ...(mediaType !== null ? { media_type: mediaType } : {}),
      ...(includeHidden ? { include_hidden: true } : {}),
    };
  } },
];

// parseSearchSort narrows ?sort= to the known SearchSort union. Unknown
// values fall through to null so the route omits the field entirely.
function parseSearchSort(raw: string | null): SearchSort | null {
  if (raw === "relevance" || raw === "newest" || raw === "oldest") return raw;
  return null;
}

// parseMediaType narrows ?media_type= to "photo" | "video". Unknown
// values fall through to null so the route omits the field.
function parseMediaType(raw: string | null): "photo" | "video" | null {
  if (raw === "photo" || raw === "video") return raw;
  return null;
}

// parseFrom narrows the ?from= query param to one of the five known
// source kinds plus the album:<id> form. Unknown values fall through
// to undefined so /media/:id?from=garbage behaves the same as no
// from at all (the lightbox treats absent from as direct-detail).
function parseFrom(raw: string | null): string | null {
  if (raw === null) return null;
  if (
    raw === "library" ||
    raw === "sessions" ||
    raw === "hidden" ||
    raw === "search" ||
    raw === "map"
  ) {
    return raw;
  }
  if (raw.startsWith("album:") && raw.length > "album:".length) return raw;
  return null;
}

// State key stored in history.state so the depth survives back/forward
// navigation. Each pushState embed the depth so popstate restores it.
const DEPTH_KEY = "__fotobank_depth__";

export class RouterStore {
  current = $state<RouteMatch>(this.match(window.location.pathname));

  // Track entries pushed by navigate() so back() only steps back when the
  // previous entry is known to be in-app. Stored in history.state so
  // browser back/forward navigation restores the correct depth (finding #12).
  private appHistoryDepth: number =
    (history.state as Record<string, unknown> | null)?.[DEPTH_KEY] as number | undefined ?? 0;

  navigate(path: string, opts?: { replace?: boolean }) {
    if (opts?.replace) {
      history.replaceState({ [DEPTH_KEY]: this.appHistoryDepth }, "", path);
    } else {
      this.appHistoryDepth += 1;
      history.pushState({ [DEPTH_KEY]: this.appHistoryDepth }, "", path);
    }
    this.syncFromLocation();
  }

  back(fallback: string) {
    if (this.appHistoryDepth > 0) {
      this.appHistoryDepth -= 1;
      window.history.back();
    } else {
      this.navigate(fallback);
    }
  }

  syncFromLocation() {
    // Restore depth from history.state on popstate navigation so back()
    // knows how many in-app entries remain (finding #12).
    const stateDepth = (history.state as Record<string, unknown> | null)?.[DEPTH_KEY];
    if (typeof stateDepth === "number") {
      this.appHistoryDepth = stateDepth;
    }
    this.current = this.match(window.location.pathname);
  }

  // Pathname-only match. window.location.pathname already excludes
  // the search and hash, so no extra stripping is needed; we read it
  // through the parameter to keep the function pure for tests.
  private match(pathname: string): RouteMatch {
    for (const p of PATTERNS) {
      const m = pathname.match(p.re);
      if (m) return p.build(m);
    }
    return { route: "notfound", path: pathname };
  }
}

// Module-level singleton. Instantiated once when this module is
// first imported (i.e., at App.svelte boot). Mirrors the
// SelectionStore module-export pattern.
export const router = new RouterStore();

// handleInternalLinkClick is the single helper used by every
// in-app anchor (Sidebar, NotFound back, MediaDetail back, MediaCell
// plain-click fallthrough). It honors the browser's existing
// "open in new tab" gestures by ignoring middle/right-click and any
// modifier key. Routing through this helper keeps the SPA from
// triggering a full-page reload on internal nav.
export function handleInternalLinkClick(
  e: MouseEvent,
  href: string,
  r: RouterStore = router,
) {
  if (e.button !== 0) return;
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  e.preventDefault();
  r.navigate(href);
}
