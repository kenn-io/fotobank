// frontend/src/lib/router/router.svelte.ts

export type RouteMatch =
  | { route: "library" }
  | { route: "sessions" }
  | { route: "settings" }
  | { route: "albums" }
  | { route: "albums.detail"; id: string }
  | { route: "shares"; album_id?: string; show_revoked?: boolean }
  | { route: "media"; id: string }
  | { route: "hidden" }
  | { route: "notfound"; path: string };

// Anchored patterns. Order doesn't matter — each regex tests in
// isolation; first match wins. Search and hash are stripped before
// matching so /media/abc?x=1 matches /media/:id.
const PATTERNS: Array<{ re: RegExp; build: (m: RegExpMatchArray) => RouteMatch }> = [
  { re: /^\/$/,           build: () => ({ route: "library" }) },
  { re: /^\/library$/,    build: () => ({ route: "library" }) },
  { re: /^\/sessions$/,   build: () => ({ route: "sessions" }) },
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
  { re: /^\/media\/([^/]+)$/, build: (m) => ({ route: "media", id: m[1]! }) },
  { re: /^\/hidden\/?$/, build: () => ({ route: "hidden" as const }) },
];

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
