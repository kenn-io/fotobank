// frontend/src/lib/router/router.svelte.ts

import type { SearchSort } from "../search/types";

export type RouteMatch =
  | {
      route: "library";
      camera?: string[];
      lens?: string[];
      facet_tag?: string[];
      has_gps?: boolean;
      media_type?: "photo" | "video";
    }
  | { route: "sessions" }
  | { route: "settings" }
  | { route: "settings.ai" }
  | { route: "admin.settings.ai" }
  | { route: "albums" }
  | { route: "albums.detail"; id: string }
  | { route: "shares"; album_id?: string; show_revoked?: boolean }
  | { route: "media"; id: string; from?: string }
  | { route: "hidden" }
  | {
      route: "map";
      z?: number;
      c?: [number, number];
      focus?: string;
      tab?: "map" | "photos";
      camera?: string[];
      lens?: string[];
      facet_tag?: string[];
      media_type?: "photo" | "video";
      // No has_gps on /map (route is geotagged-only).
    }
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
      camera?: string[];
      lens?: string[];
      facet_tag?: string[];
      has_gps?: boolean;
    }
  | { route: "notfound"; path: string };

// parseFilterParams reads the multi-value sidebar facet params shared
// across /library, /map, and /search. camera, lens, and facet_tag use
// the repeat convention (?camera=A&camera=B → ["A","B"]); empty
// repetitions are kept (URLSearchParams.getAll preserves them) but the
// field is omitted entirely when no values are present so a bare route
// stays bare. media_type narrows to "photo"|"video" — unknown values
// fall through (the field is omitted).
function parseFilterParams(sp: URLSearchParams): {
  camera?: string[];
  lens?: string[];
  facet_tag?: string[];
  media_type?: "photo" | "video";
} {
  const out: {
    camera?: string[];
    lens?: string[];
    facet_tag?: string[];
    media_type?: "photo" | "video";
  } = {};
  const camera = sp.getAll("camera");
  if (camera.length > 0) out.camera = camera;
  const lens = sp.getAll("lens");
  if (lens.length > 0) out.lens = lens;
  const facetTag = sp.getAll("facet_tag");
  if (facetTag.length > 0) out.facet_tag = facetTag;
  const mt = sp.get("media_type");
  if (mt === "photo" || mt === "video") out.media_type = mt;
  return out;
}

// parseHasGps narrows ?has_gps= to boolean. The SPA-canonical wire
// format is "1"/"0" (composes cleanly with the rest of the filter
// chip URL surface), but the backend's `/api/v1/facets` and
// `/api/v1/media` accept "true"/"false" as well, so a bookmarked or
// hand-typed URL using the backend form would otherwise drop the
// filter. Accept both case-insensitively; the URL writer keeps
// emitting the canonical "1"/"0" form. Anything else → undefined.
function parseHasGps(sp: URLSearchParams): boolean | undefined {
  const v = sp.get("has_gps")?.toLowerCase();
  if (v === "1" || v === "true") return true;
  if (v === "0" || v === "false") return false;
  return undefined;
}

// buildLibraryRoute is shared by `^/$` and `^/library$`; the two URLs
// resolve to the same in-app surface so they parse the same query
// params. Kept as a helper to avoid drift between the two slots.
function buildLibraryRoute(): RouteMatch {
  const sp = new URLSearchParams(window.location.search);
  const filters = parseFilterParams(sp);
  const hasGps = parseHasGps(sp);
  return {
    route: "library" as const,
    ...filters,
    ...(hasGps !== undefined ? { has_gps: hasGps } : {}),
  };
}

// Anchored patterns. Order doesn't matter — each regex tests in
// isolation; first match wins. Search and hash are stripped before
// matching so /media/abc?x=1 matches /media/:id.
const PATTERNS: Array<{ re: RegExp; build: (m: RegExpMatchArray) => RouteMatch }> = [
  { re: /^\/$/,           build: () => buildLibraryRoute() },
  { re: /^\/library$/,    build: () => buildLibraryRoute() },
  { re: /^\/sessions$/,   build: () => ({ route: "sessions" }) },
  { re: /^\/admin\/settings\/ai\/?$/, build: () => ({ route: "admin.settings.ai" }) },
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
  // /map accepts ?z, ?c=lat,lng, ?focus=<id>, ?tab=map|photos.
  // Invalid numeric params drop out (Number.isFinite check); unknown
  // tab values fall through so they don't poison the map shell.
  { re: /^\/map\/?$/, build: () => {
    const sp = new URLSearchParams(window.location.search);
    const zRaw = sp.get("z");
    const cRaw = sp.get("c");
    const focusRaw = sp.get("focus");
    const tabRaw = sp.get("tab");
    // toFiniteNumber rejects empty/whitespace tokens because Number("") and
    // Number(" ") both return 0, which would silently coerce ?z= to z:0
    // and ?c=, to c:[0,0] — meaningful coordinates we never intended.
    const z = toFiniteNumber(zRaw);
    const cParts = cRaw !== null
      ? cRaw.split(",").map((s) => toFiniteNumber(s))
      : [];
    const tab = tabRaw === "photos" || tabRaw === "map" ? tabRaw : null;
    const filters = parseFilterParams(sp);
    return {
      route: "map" as const,
      ...(Number.isFinite(z) ? { z } : {}),
      ...(cParts.length === 2 && cParts.every(Number.isFinite)
        ? { c: [cParts[0]!, cParts[1]!] as [number, number] }
        : {}),
      ...(focusRaw !== null && focusRaw !== "" ? { focus: focusRaw } : {}),
      ...(tab !== null ? { tab } : {}),
      ...filters,
      // No has_gps on /map: the route is geotagged-only by definition,
      // so the toggle would be a no-op. The sidebar suppresses the
      // chip on /map; the parser ignores any inbound has_gps query.
    };
  } },
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
    const includeHidden = sp.get("include_hidden") === "true";
    // parseFilterParams provides camera/lens/facet_tag/media_type using
    // the same "photo"|"video" narrower as /library and /map, so /search
    // gets media_type from the shared helper instead of a local copy.
    const filters = parseFilterParams(sp);
    const hasGps = parseHasGps(sp);
    return {
      route: "search" as const,
      ...(q != null && q !== "" ? { q } : {}),
      ...(sort !== null ? { sort } : {}),
      ...(dateAfter != null && dateAfter !== "" ? { date_after: dateAfter } : {}),
      ...(dateBefore != null && dateBefore !== "" ? { date_before: dateBefore } : {}),
      ...(tags.length > 0 ? { tag: tags } : {}),
      ...(location != null && location !== "" ? { location } : {}),
      ...(includeHidden ? { include_hidden: true } : {}),
      ...filters,
      ...(hasGps !== undefined ? { has_gps: hasGps } : {}),
    };
  } },
];

// toFiniteNumber returns a finite number from a query token, or NaN
// for empty/whitespace/non-numeric input. Plain Number("") is 0 and
// Number(" ") is 0, so callers using `Number(raw) → finite check`
// would silently accept blank tokens as zero coordinates. This wrapper
// rejects them so /map?z=&c=, falls through to fitToAll instead of
// centering on lat=0,lng=0,zoom=0 (the Atlantic, fully zoomed out).
function toFiniteNumber(raw: string | null): number {
  if (raw === null) return Number.NaN;
  const trimmed = raw.trim();
  if (trimmed === "") return Number.NaN;
  const n = Number(trimmed);
  return Number.isFinite(n) ? n : Number.NaN;
}

// parseSearchSort narrows ?sort= to the known SearchSort union. Unknown
// values fall through to null so the route omits the field entirely.
function parseSearchSort(raw: string | null): SearchSort | null {
  if (raw === "relevance" || raw === "newest" || raw === "oldest") return raw;
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
