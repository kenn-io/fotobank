import { describe, it, expect } from "vitest";
import { groupIntoSessions } from "./sessionGrouping";
import type { Media } from "../media/mediaStore.svelte";

const m = (id: string, iso: string): Media => ({
  id, timestamp: iso, taken: new Date(iso), aspect: 1, thumbUrl: "", thumbVersion: 0,
});

describe("groupIntoSessions", () => {
  it("starts a new session when gap exceeds threshold", () => {
    const items = [
      m("a", "2026-04-18T10:00:00Z"),
      m("b", "2026-04-18T10:30:00Z"),
      m("c", "2026-04-18T18:00:00Z"), // 7.5h gap
      m("d", "2026-04-19T01:00:00Z"), // 7h gap
    ];
    const sessions = groupIntoSessions(items, { gapHours: 4 });
    expect(sessions).toHaveLength(3);
    // Outer order is newest-session-first to match mediaStore's
    // descending month layout. Items within each session stay in
    // chronological order so a beach trip reads forward in time.
    expect(sessions[0]?.items.map(i => i.id)).toEqual(["d"]);
    expect(sessions[1]?.items.map(i => i.id)).toEqual(["c"]);
    expect(sessions[2]?.items.map(i => i.id)).toEqual(["a", "b"]);
  });

  it("returns empty for empty input", () => {
    expect(groupIntoSessions([], { gapHours: 4 })).toEqual([]);
  });
});
