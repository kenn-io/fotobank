import { describe, it, expect } from "vitest";
import { shouldRedirectSharesToHome } from "./routeGuards";
import type { RouteMatch } from "../router/router.svelte";

const SHARES: RouteMatch = { route: "shares" };
const LIBRARY: RouteMatch = { route: "library" };

describe("shouldRedirectSharesToHome", () => {
  it("returns true on /shares when sharing is disabled and config ready", () => {
    expect(
      shouldRedirectSharesToHome(SHARES, { ready: true, sharingEnabled: false }),
    ).toBe(true);
  });

  it("returns false on /shares when sharing is enabled", () => {
    expect(
      shouldRedirectSharesToHome(SHARES, { ready: true, sharingEnabled: true }),
    ).toBe(false);
  });

  it("returns false on /shares while config is still loading", () => {
    // ready=false means /me hasn't resolved yet. Holding the redirect
    // until ready prevents a flash-redirect on a fresh boot when the
    // user lands directly on /shares.
    expect(
      shouldRedirectSharesToHome(SHARES, { ready: false, sharingEnabled: false }),
    ).toBe(false);
  });

  it("returns false for non-shares routes regardless of sharing flag", () => {
    expect(
      shouldRedirectSharesToHome(LIBRARY, { ready: true, sharingEnabled: false }),
    ).toBe(false);
    expect(
      shouldRedirectSharesToHome(LIBRARY, { ready: true, sharingEnabled: true }),
    ).toBe(false);
  });

  it("ignores extra fields on the shares discriminant", () => {
    // /shares?album_id=… is still the shares route; the guard fires
    // the same way.
    const sharesWithAlbum: RouteMatch = { route: "shares", album_id: "a1" };
    expect(
      shouldRedirectSharesToHome(sharesWithAlbum, {
        ready: true,
        sharingEnabled: false,
      }),
    ).toBe(true);
  });
});
