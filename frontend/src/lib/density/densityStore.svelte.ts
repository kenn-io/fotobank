import type { Client } from "../api/client";

export type Preset = "compact" | "comfortable" | "large";

export const ROW_HEIGHTS: Record<Preset, number> = {
  compact: 140,
  comfortable: 200,
  large: 280,
};

const ORDER: Preset[] = ["compact", "comfortable", "large"];

// DensityStore is a Svelte 5 rune-backed store. It persists the
// selected preset per context (e.g. "library") under the user_settings
// key `density.<context>` and exposes the resolved targetRowHeight for
// the justified layout.
export class DensityStore {
  preset = $state<Preset>("comfortable");
  loaded = $state(false);

  constructor(
    private client: Pick<Client, "GET" | "PUT">,
    private context: string,
  ) {}

  get targetRowHeight(): number {
    return ROW_HEIGHTS[this.preset];
  }

  async load() {
    const res = await this.client.GET("/api/v1/settings/user/{key}", {
      params: { path: { key: `density.${this.context}` } },
    });
    if (res.data?.value) {
      try {
        const v = JSON.parse(res.data.value);
        if (v === "compact" || v === "comfortable" || v === "large") {
          this.preset = v;
        }
      } catch {
        // Ignore malformed value; keep current preset.
      }
    }
    this.loaded = true;
  }

  async set(p: Preset) {
    this.preset = p;
    await this.client.PUT("/api/v1/settings/user/{key}", {
      params: { path: { key: `density.${this.context}` } },
      body: { value: JSON.stringify(p) },
    });
  }

  nudge(delta: 1 | -1) {
    const i = ORDER.indexOf(this.preset);
    const next = ORDER[Math.max(0, Math.min(ORDER.length - 1, i + delta))];
    if (next && next !== this.preset) {
      void this.set(next);
    }
  }
}
