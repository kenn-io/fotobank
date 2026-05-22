// Pure route-guard predicates used by App.svelte.
//
// Extracted from the component so the predicate can be unit-tested
// without instantiating the full App tree. The component's $effect
// calls into this and triggers the navigation; the predicate itself
// is side-effect free.
//
// AppConfigStore exposes `ready` (defaults false; flips to true after
// the /me load resolves, success OR failure — see appConfig.svelte.ts)
// and `sharingEnabled`. The guard only fires once `ready` is true so a
// fresh boot doesn't flash-redirect before /me settles.

import type { RouteMatch } from "../router/router.svelte";

export interface SharingGate {
  ready: boolean;
  sharingEnabled: boolean;
}

// shouldRedirectSharesToHome returns true when the SPA is currently
// rendering /shares but the sharing UI feature is disabled. Callers
// should react by navigating to "/" with replace:true.
export function shouldRedirectSharesToHome(
  route: RouteMatch,
  gate: SharingGate,
): boolean {
  return route.route === "shares" && gate.ready && !gate.sharingEnabled;
}
