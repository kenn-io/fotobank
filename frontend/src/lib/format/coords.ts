// lat=0 formats as N, lon=0 as E so tests stay deterministic; the
// null-island case never reaches here (exifread drops it on extraction).
export function formatCoord(lat: number, lon: number): string {
  const ns = lat >= 0 ? "N" : "S";
  const ew = lon >= 0 ? "E" : "W";
  return `${Math.abs(lat).toFixed(4)}° ${ns}, ${Math.abs(lon).toFixed(4)}° ${ew}`;
}
