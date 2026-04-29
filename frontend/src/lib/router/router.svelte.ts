// frontend/src/lib/router/router.svelte.ts

export type RouteMatch =
  | { route: "library" }
  | { route: "sessions" }
  | { route: "settings" }
  | { route: "albums" }
  | { route: "albums.detail"; id: string }
  | { route: "shares" }
  | { route: "media"; id: string }
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
  { re: /^\/shares\/?$/,  build: () => ({ route: "shares" }) },
  { re: /^\/media\/([^/]+)$/, build: (m) => ({ route: "media", id: m[1]! }) },
];

export class RouterStore {
  current = $state<RouteMatch>(this.match(window.location.pathname));

  navigate(path: string) {
    history.pushState({}, "", path);
    this.syncFromLocation();
  }

  syncFromLocation() {
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
