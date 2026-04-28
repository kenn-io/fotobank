import { describe, it, expect } from "vitest";
import { formatCoord } from "./coords";

describe("formatCoord", () => {
  it("northern + eastern hemisphere", () => {
    expect(formatCoord(48.8566, 2.3522)).toBe("48.8566° N, 2.3522° E");
  });
  it("southern + western hemisphere", () => {
    expect(formatCoord(-33.9249, -70.6483)).toBe("33.9249° S, 70.6483° W");
  });
  it("northern + western (e.g. NYC)", () => {
    expect(formatCoord(40.7128, -74.006)).toBe("40.7128° N, 74.0060° W");
  });
  it("zero hemispheres default N / E", () => {
    expect(formatCoord(0, 0)).toBe("0.0000° N, 0.0000° E");
  });
  it("rounds to 4 decimal places", () => {
    expect(formatCoord(48.85664567, 2.35224567)).toBe("48.8566° N, 2.3522° E");
  });
});
