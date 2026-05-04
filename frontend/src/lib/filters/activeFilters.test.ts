import { describe, it, expect } from "vitest";
import {
  fromRoute,
  withToggled,
  withFilters,
  filterKey,
  isEmpty,
  type ActiveFilters,
} from "./activeFilters";

describe("activeFilters.fromRoute", () => {
  it("library with no filters → empty", () => {
    expect(fromRoute({ route: "library" })).toEqual({
      cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    });
  });

  it("library with all five facets", () => {
    expect(fromRoute({
      route: "library",
      camera: ["Sony A7R IV"],
      lens: ["FE 24-70mm F2.8 GM"],
      facet_tag: ["dog"],
      has_gps: true,
      media_type: "photo",
    })).toEqual({
      cameras: ["Sony A7R IV"],
      lenses: ["FE 24-70mm F2.8 GM"],
      tagKeys: ["dog"],
      hasGps: true,
      mediaType: "photo",
    });
  });

  it("non-filter routes return empty", () => {
    expect(fromRoute({ route: "albums" })).toEqual({
      cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    });
  });
});

describe("activeFilters.withToggled", () => {
  const empty: ActiveFilters = {
    cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
  };

  it("adds a camera when not present", () => {
    expect(withToggled(empty, "cameras", "Sony A7R IV").cameras).toEqual(["Sony A7R IV"]);
  });

  it("removes a camera when already present", () => {
    const f = { ...empty, cameras: ["Sony A7R IV", "Canon EOS R5"] };
    expect(withToggled(f, "cameras", "Sony A7R IV").cameras).toEqual(["Canon EOS R5"]);
  });

  it("hasGps toggles tri-state nil → true → null", () => {
    const a = withToggled(empty, "hasGps", "true");
    expect(a.hasGps).toBe(true);
    const b = withToggled(a, "hasGps", "true");
    expect(b.hasGps).toBe(null);
    const c = withToggled(empty, "hasGps", "false");
    expect(c.hasGps).toBe(false);
  });

  it("mediaType toggles between photo, video, null", () => {
    const a = withToggled(empty, "mediaType", "photo");
    expect(a.mediaType).toBe("photo");
    const b = withToggled(a, "mediaType", "photo");
    expect(b.mediaType).toBe(null);
  });
});

describe("activeFilters.withFilters", () => {
  it("preserves non-filter params", () => {
    const cur = new URLSearchParams("q=mountain&sort=score&date_after=2024-01-01");
    const f: ActiveFilters = {
      cameras: ["Sony A7R IV"], lenses: [], tagKeys: ["dog"],
      hasGps: true, mediaType: null,
    };
    const sp = withFilters(cur, f);
    expect(sp.get("q")).toBe("mountain");
    expect(sp.get("sort")).toBe("score");
    expect(sp.get("date_after")).toBe("2024-01-01");
    expect(sp.getAll("camera")).toEqual(["Sony A7R IV"]);
    expect(sp.getAll("facet_tag")).toEqual(["dog"]);
    expect(sp.get("has_gps")).toBe("1");
  });

  it("strips filter params not present in new filters", () => {
    const cur = new URLSearchParams("camera=Old&lens=Stale");
    const sp = withFilters(cur, {
      cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    });
    expect(sp.toString()).toBe("");
  });

  it("encodes hasGps=false as has_gps=0", () => {
    const sp = withFilters(new URLSearchParams(), {
      cameras: [], lenses: [], tagKeys: [], hasGps: false, mediaType: null,
    });
    expect(sp.get("has_gps")).toBe("0");
  });
});

describe("activeFilters.filterKey", () => {
  it("two equivalent filter sets produce the same key", () => {
    const a: ActiveFilters = {
      cameras: ["Sony", "Canon"], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    };
    const b: ActiveFilters = {
      cameras: ["Canon", "Sony"], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    };
    expect(filterKey(a)).toBe(filterKey(b));
  });
  it("different filter sets produce different keys", () => {
    const a: ActiveFilters = {
      cameras: ["Sony"], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    };
    const b: ActiveFilters = {
      cameras: ["Canon"], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    };
    expect(filterKey(a)).not.toBe(filterKey(b));
  });
});

describe("activeFilters.isEmpty", () => {
  it("empty filters", () => {
    expect(isEmpty({ cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null })).toBe(true);
  });
  it("non-empty cameras", () => {
    expect(isEmpty({ cameras: ["Sony"], lenses: [], tagKeys: [], hasGps: null, mediaType: null })).toBe(false);
  });
  it("hasGps=false counts as active", () => {
    expect(isEmpty({ cameras: [], lenses: [], tagKeys: [], hasGps: false, mediaType: null })).toBe(false);
  });
});
