import type { Client } from "../api/client";

// AIInspectionStore is a Svelte 5 rune-backed store for the per-user
// "AI Inspection" toggle. Mirrors the densityStore pattern: load() pulls
// the persisted JSON value at boot, set() flips the local state and PUTs
// the new value, and a dirty flag protects an eager click from a late
// server response.
//
// Wire format: the user-settings route stores opaque JSON strings under
// the "ai.inspection" key. This store writes the literal `true` / `false`
// JSON booleans; the server-side typed accessor in
// internal/service/usersettings only honours the literal `true`. Other
// callers (e.g. the SettingsAI route) read the same key through this
// store rather than poking the API directly.
//
// Default: false. The setting is opt-in — diagnostics carry score
// breakdowns the casual user does not want surfaced in the UI, and
// shipping it on for everyone would change the default search shape.
export class AIInspectionStore {
  enabled = $state<boolean>(false);
  loaded = $state(false);
  // load() is fired at boot; if the user clicks the toggle before the
  // GET resolves the late server response must not stomp the newer
  // choice. Same pattern as densityStore.
  private dirty = false;

  constructor(private client: Pick<Client, "getUserSetting" | "putUserSetting">) {}

  async load(): Promise<void> {
    try {
      const res = await this.client.getUserSetting("ai.inspection");
      if (!this.dirty && res.data?.value !== undefined) {
        try {
          const parsed = JSON.parse(res.data.value);
          if (typeof parsed === "boolean") {
            this.enabled = parsed;
          }
        } catch {
          // Malformed payload — keep the default (false).
        }
      }
    } finally {
      // loaded must flip to true even when the GET rejects so the
      // search page's hydration $effect (which gates on loaded) still
      // proceeds. Persisted state is best-effort; transport failures
      // shouldn't strand the rest of the UI on the load barrier.
      this.loaded = true;
    }
  }

  async set(on: boolean): Promise<void> {
    this.enabled = on;
    this.dirty = true;
    await this.client.putUserSetting("ai.inspection", { value: JSON.stringify(on) });
  }
}
