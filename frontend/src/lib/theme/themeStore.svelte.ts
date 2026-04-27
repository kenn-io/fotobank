import type { Client } from "../api/client";

export type Theme = "system" | "light" | "dark";

// ThemeStore is a Svelte 5 rune-backed store. It loads the persisted
// theme on construction (via load()) and persists overrides via set().
// It also keeps the document classList in sync with the current theme
// so the CSS variables in app.css respond.
export class ThemeStore {
  theme = $state<Theme>("system");
  loaded = $state(false);
  // App.svelte fires load() at boot. If a system-prefers-color-scheme
  // listener or another caller flips theme via set() before that GET
  // resolves, the late server response must not stomp the newer
  // choice. Same pattern as densityStore; see its dirty-flag comment.
  private dirty = false;

  constructor(private client: Pick<Client, "GET" | "PUT">) {}

  async load() {
    const res = await this.client.GET("/api/v1/settings/user/{key}", {
      params: { path: { key: "theme" } },
    });
    if (!this.dirty && res.data?.value) {
      try {
        const parsed = JSON.parse(res.data.value);
        if (parsed === "light" || parsed === "dark" || parsed === "system") {
          this.theme = parsed;
        }
      } catch {
        // Ignore malformed value; stay on system.
      }
    }
    this.loaded = true;
    this.apply();
  }

  async set(theme: Theme) {
    this.theme = theme;
    this.dirty = true;
    this.apply();
    await this.client.PUT("/api/v1/settings/user/{key}", {
      params: { path: { key: "theme" } },
      body: { value: JSON.stringify(theme) },
    });
  }

  private apply() {
    if (typeof document === "undefined") return; // SSR / test guard
    const root = document.documentElement;
    root.classList.remove("theme-light", "theme-dark");
    if (this.theme === "light") root.classList.add("theme-light");
    if (this.theme === "dark") root.classList.add("theme-dark");
  }
}
