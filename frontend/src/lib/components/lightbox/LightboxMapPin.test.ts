import { render, fireEvent } from "@testing-library/svelte";
import { describe, expect, it, vi } from "vitest";
import LightboxMapPin from "./LightboxMapPin.svelte";

type Coord = number | null | undefined;

type PinMedia = {
  id: string;
  latitude: Coord;
  longitude: Coord;
  location_label: string;
};

type PinProps = {
  media: PinMedia;
  navigate?: (href: string) => void;
};

function pinProps(
  media: { id: string; latitude: Coord; longitude: Coord },
  overrides: Partial<PinProps> = {},
): PinProps {
  return { media: { ...media, location_label: "" }, ...overrides };
}

describe("LightboxMapPin", () => {
  it("renders nothing when latitude is null", () => {
    const { queryByTestId } = render(LightboxMapPin, {
      props: pinProps({ id: "x", latitude: null, longitude: 12.3 }),
    });
    expect(queryByTestId("lightbox-map-pin")).toBeNull();
  });

  it("renders nothing when longitude is undefined", () => {
    const { queryByTestId } = render(LightboxMapPin, {
      props: pinProps({ id: "x", latitude: 12.3, longitude: undefined }),
    });
    expect(queryByTestId("lightbox-map-pin")).toBeNull();
  });

  it("renders when both lat and lon are exactly 0 (regression)", () => {
    const { getByTestId } = render(LightboxMapPin, {
      props: pinProps({ id: "x", latitude: 0, longitude: 0 }),
    });
    expect(getByTestId("lightbox-map-pin")).toBeTruthy();
  });

  it("plain click navigates to /map?focus=<id>", () => {
    const navigate = vi.fn();
    const { getByTestId } = render(LightboxMapPin, {
      props: pinProps({ id: "abc", latitude: 1, longitude: 2 }, { navigate }),
    });
    // Scope by testid: Leaflet's attribution control adds its own <a>
    // links, so getByRole("link") would find multiple matches.
    const link = getByTestId("lightbox-map-pin") as HTMLAnchorElement;
    fireEvent.click(link, { button: 0 });
    expect(navigate).toHaveBeenCalledWith("/map?z=14&c=1,2&focus=abc");
  });

  it("cmd-click does NOT intercept (lets the browser open in a new tab)", () => {
    const navigate = vi.fn();
    const { getByTestId } = render(LightboxMapPin, {
      props: pinProps({ id: "abc", latitude: 1, longitude: 2 }, { navigate }),
    });
    const link = getByTestId("lightbox-map-pin") as HTMLAnchorElement;
    fireEvent.click(link, { button: 0, metaKey: true });
    expect(navigate).not.toHaveBeenCalled();
  });
});
