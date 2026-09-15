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
  // Routes call load() at mount and the user can click a preset before
  // that GET resolves. Without this guard the late server response
  // could overwrite the user's fresher choice. Once set() has run, the
  // local value is canonical for this session — load() must not stomp
  // it.
  private dirty = false;

  constructor(
    private client: Pick<Client, "getUserSetting" | "putUserSetting">,
    private context: string,
  ) {}

  get targetRowHeight(): number {
    return ROW_HEIGHTS[this.preset];
  }

  async load() {
    const res = await this.client.getUserSetting(`density.${this.context}`);
    if (!this.dirty && res.data?.value) {
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
    this.dirty = true;
    await this.client.putUserSetting(`density.${this.context}`, { value: JSON.stringify(p) });
  }

  nudge(delta: 1 | -1) {
    const i = ORDER.indexOf(this.preset);
    const next = ORDER[Math.max(0, Math.min(ORDER.length - 1, i + delta))];
    if (next && next !== this.preset) {
      void this.set(next);
    }
  }
}
