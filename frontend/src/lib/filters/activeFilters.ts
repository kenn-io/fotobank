import type { RouteMatch } from "../router/router.svelte";

export type FacetGroup = "cameras" | "lenses" | "tagKeys" | "hasGps" | "mediaType";

export type ActiveFilters = {
  cameras: string[];
  lenses: string[];
  tagKeys: string[];          // sourced from facet_tag URL param (NOT tag)
  hasGps: boolean | null;     // null = no filter applied
  mediaType: "photo" | "video" | null;
};

// emptyFilters is a factory, not a constant. A spread of a shared
// constant ({ ...EMPTY }) shallow-copies the wrapper but leaves
// `cameras`, `lenses`, and `tagKeys` pointing at the constant's own
// arrays — any in-place mutation downstream would permanently
// poison the "empty" baseline for every subsequent caller. The
// factory hands every caller a fresh wrapper plus fresh arrays.
function emptyFilters(): ActiveFilters {
  return {
    cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
  };
}

const FILTER_PARAM_KEYS = ["camera", "lens", "facet_tag", "has_gps", "media_type"];

/** Extract the active filter set from the route. Non-filter routes return an empty filter set. */
export function fromRoute(r: RouteMatch): ActiveFilters {
  if (r.route !== "library" && r.route !== "search" && r.route !== "map") {
    return emptyFilters();
  }
  const f = emptyFilters();
  if ("camera" in r && r.camera) f.cameras = [...r.camera];
  if ("lens" in r && r.lens) f.lenses = [...r.lens];
  if ("facet_tag" in r && r.facet_tag) f.tagKeys = [...r.facet_tag];
  if ("has_gps" in r && r.has_gps !== undefined) f.hasGps = r.has_gps;
  if ("media_type" in r && r.media_type) f.mediaType = r.media_type;
  return f;
}

/**
 * Toggle a single value within a facet group. For multi-select groups
 * (cameras/lenses/tagKeys) adds/removes the value. For single-select
 * groups (hasGps/mediaType) toggles between the value and null.
 */
export function withToggled(f: ActiveFilters, group: FacetGroup, value: string): ActiveFilters {
  switch (group) {
    case "cameras":
    case "lenses":
    case "tagKeys": {
      const cur = f[group];
      const next = cur.includes(value) ? cur.filter((v) => v !== value) : [...cur, value];
      return { ...f, [group]: next };
    }
    case "hasGps": {
      const desired = value === "true" ? true : false;
      return { ...f, hasGps: f.hasGps === desired ? null : desired };
    }
    case "mediaType": {
      if (value !== "photo" && value !== "video") return f;
      return { ...f, mediaType: f.mediaType === value ? null : value };
    }
  }
}

/**
 * Merge `f` into `current`, preserving non-filter params (q, sort,
 * date_after, etc.). Strips the known filter param keys before
 * re-applying so toggling off a filter does the right thing.
 */
export function withFilters(current: URLSearchParams, f: ActiveFilters): URLSearchParams {
  const sp = new URLSearchParams(current);
  for (const k of FILTER_PARAM_KEYS) sp.delete(k);
  for (const v of f.cameras) sp.append("camera", v);
  for (const v of f.lenses) sp.append("lens", v);
  for (const v of f.tagKeys) sp.append("facet_tag", v);
  if (f.hasGps !== null) sp.set("has_gps", f.hasGps ? "1" : "0");
  if (f.mediaType !== null) sp.set("media_type", f.mediaType);
  return sp;
}

/** Stable key for caching: sort multi-value lists deterministically. */
export function filterKey(f: ActiveFilters): string {
  const norm = {
    cameras: [...f.cameras].sort(),
    lenses: [...f.lenses].sort(),
    tagKeys: [...f.tagKeys].sort(),
    hasGps: f.hasGps,
    mediaType: f.mediaType,
  };
  return JSON.stringify(norm);
}

export function isEmpty(f: ActiveFilters): boolean {
  return (
    f.cameras.length === 0 &&
    f.lenses.length === 0 &&
    f.tagKeys.length === 0 &&
    f.hasGps === null &&
    f.mediaType === null
  );
}
