import type { Client } from "../api/client";

export type Theme = "system" | "light" | "dark";

// ThemeStore is a Svelte 5 rune-backed store. It loads the persisted
// theme on construction (via load()) and persists overrides via set().
// It also keeps the document classList in sync with the current theme
// so the CSS variables in app.css respond.
export class ThemeStore {
  theme = $state<Theme>("system");
  loaded = $state(false);

  constructor(private client: Pick<Client, "GET" | "PUT">) {}

  async load() {
    const res = await this.client.GET("/api/v1/settings/user/{key}", {
      params: { path: { key: "theme" } },
    });
    if (res.data?.value) {
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
