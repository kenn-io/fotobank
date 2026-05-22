// frontend/src/lib/map/tiles.ts
//
// Single source of truth for the SPA's map tile URL and attribution.
// Centralizing the URL satisfies the OSMF tile-policy recommendation
// against hardcoding the URL across an app, and makes provider
// migration (self-hosted, paid CDN, etc.) one-file.
//
// Reference: https://operations.osmfoundation.org/policies/tiles/

export function tileUrl(): string {
  return "https://tile.openstreetmap.org/{z}/{x}/{y}.png";
}

export function attribution(): string {
  return '© <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener">OpenStreetMap</a> contributors';
}

// Standard OSM raster tiles top out at zoom 19. Pinned here so the
// MapPane can pass it to Leaflet's L.tileLayer({ maxZoom }).
export const defaultMaxZoom = 19;
