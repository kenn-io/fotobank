import { describe, expect, it } from "vitest";
import { tileUrl, attribution, defaultMaxZoom } from "./tiles";

describe("tiles", () => {
  it("returns the OSM raster tile URL pattern", () => {
    expect(tileUrl()).toBe("https://tile.openstreetmap.org/{z}/{x}/{y}.png");
  });

  it("returns OSM attribution string", () => {
    expect(attribution()).toContain("OpenStreetMap");
    expect(attribution()).toContain("contributors");
  });

  it("exports a default max zoom", () => {
    // OSM standard tiles top out at z=19.
    expect(defaultMaxZoom).toBe(19);
  });
});
