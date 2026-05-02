// frontend/src/lib/app/appConfig.svelte.ts
//
// Reactive snapshot of /api/v1/me's `features` block, fetched once on
// App.svelte mount. Components read `appConfig.sharingEnabled`
// reactively to gate UI surfaces (Sidebar, MediaActions, Album CTAs,
// /shares route).
//
// Defensive defaults: sharingEnabled=false, ready=false. After load()
// returns, ready is ALWAYS true — even on HTTP or network failure —
// because the safe behavior is "treat failure as sharing disabled" so
// route guards (e.g. /shares redirect) can fire predictably. A pending
// load leaves the SPA on the safer default (sharing UI hidden).

import type { Client } from "../api/client";

export class AppConfigStore {
  sharingEnabled = $state(false);
  ready = $state(false);

  constructor(private client: Pick<Client, "GET">) {}

  async load(): Promise<void> {
    try {
      const { data } = await this.client.GET("/api/v1/me", {});
      // openapi-fetch returns data===undefined for non-2xx responses;
      // both HTTP error and "no body" are treated as resolved-disabled.
      if (!data) {
        this.sharingEnabled = false;
        this.ready = true;
        return;
      }
      this.sharingEnabled = data.features.sharing_enabled === true;
      this.ready = true;
    } catch {
      // Network failure (fetch threw): same treatment as HTTP failure.
      this.sharingEnabled = false;
      this.ready = true;
    }
  }
}
