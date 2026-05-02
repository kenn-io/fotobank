// frontend/src/lib/app/appConfig.svelte.ts
//
// Reactive snapshot of /api/v1/me, fetched once on App.svelte mount.
// Two surfaces today: `sharingEnabled` gates sharing UI, and `principal`
// carries the active identity (hub / user_id / handle) so AppHeader and
// any future identity affordances render the real owner instead of a
// hardcoded placeholder.
//
// Defensive defaults: sharingEnabled=false, principal=null, ready=false.
// After load() returns, ready is ALWAYS true — even on HTTP or network
// failure — because the safe behavior is "treat failure as sharing
// disabled" so route guards (e.g. /shares redirect) can fire
// predictably. A pending load leaves the SPA on the safer default
// (sharing UI hidden, identity blank).

import type { Client } from "../api/client";

export type Principal = {
  hub: string;
  userId: string;
  handle: string;
};

export class AppConfigStore {
  sharingEnabled = $state(false);
  principal = $state<Principal | null>(null);
  ready = $state(false);

  constructor(private client: Pick<Client, "GET">) {}

  async load(): Promise<void> {
    try {
      const { data } = await this.client.GET("/api/v1/me", {});
      // openapi-fetch returns data===undefined for non-2xx responses;
      // both HTTP error and "no body" are treated as resolved-disabled.
      if (!data) {
        this.sharingEnabled = false;
        this.principal = null;
        this.ready = true;
        return;
      }
      this.sharingEnabled = data.features.sharing_enabled === true;
      // Handle is optional in the API schema (PrincipalStruct.handle?: string).
      // For UI display, fall back to user_id so AppHeader always has a
      // non-empty string — a blank "dev-local: " in the header would
      // look like a rendering bug rather than a deliberate empty value.
      this.principal = {
        hub: data.principal.hub,
        userId: data.principal.user_id,
        handle: data.principal.handle ?? data.principal.user_id,
      };
      this.ready = true;
    } catch {
      // Network failure (fetch threw): same treatment as HTTP failure.
      this.sharingEnabled = false;
      this.principal = null;
      this.ready = true;
    }
  }
}
