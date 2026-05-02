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

export class AppConfigStore {
  private _sharingEnabled = $state(false);
  private _ready = $state(false);

  get sharingEnabled(): boolean {
    return this._sharingEnabled;
  }

  get ready(): boolean {
    return this._ready;
  }

  async load(): Promise<void> {
    try {
      const resp = await fetch("/api/v1/me");
      if (!resp.ok) {
        // Treat HTTP failure as resolved-disabled so guards can fire.
        this._sharingEnabled = false;
        this._ready = true;
        return;
      }
      const body = (await resp.json()) as {
        features?: { sharing_enabled?: boolean };
      };
      this._sharingEnabled = body?.features?.sharing_enabled === true;
      this._ready = true;
    } catch {
      // Network failure: same treatment as HTTP failure.
      this._sharingEnabled = false;
      this._ready = true;
    }
  }
}

export const appConfig = new AppConfigStore();
