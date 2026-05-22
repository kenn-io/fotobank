import { describe, it, expect } from "vitest";
import { render } from "@testing-library/svelte";
import IdentityChips from "./IdentityChips.svelte";
import type { Principal } from "../app/appConfig.svelte";

const principal: Principal = {
  hub: "dev-local",
  userId: "owner",
  handle: "owner",
};

describe("IdentityChips", () => {
  it("renders nothing when ready=false", () => {
    const { queryByTestId } = render(IdentityChips, {
      props: { principal, ready: false },
    });
    expect(queryByTestId("id-chip-hub")).toBeNull();
    expect(queryByTestId("id-chip-user")).toBeNull();
  });

  it("renders both chips when ready=true with the principal's hub and handle", () => {
    const { getByTestId } = render(IdentityChips, {
      props: { principal, ready: true },
    });
    const hub = getByTestId("id-chip-hub");
    const user = getByTestId("id-chip-user");
    // Use textContent and assert the value is present rather than
    // checking exact equality — the chip wrapper also includes its
    // label text ("HUB"/"USER") which would interfere with `===`.
    expect(hub.textContent).toContain("dev-local");
    expect(user.textContent).toContain("owner");
  });

  it("HUB status dot flips to danger color when error=true", () => {
    // Render the same chip twice (default + error) and compare the
    // dot's resolved background. The dot is a token-driven inline
    // style, so we read background to confirm the token swap.
    // Scope each query to its own render's `container` — getByTestId
    // would otherwise see both renders and trip on duplicate ids.
    const ok = render(IdentityChips, {
      props: { principal, ready: true },
    });
    const okHub = ok.container.querySelector(
      '[data-testid="id-chip-hub"]',
    ) as HTMLElement | null;
    expect(okHub).not.toBeNull();
    const okDot = okHub?.querySelector(".id-chip-dot") as HTMLElement | null;
    expect(okDot).not.toBeNull();
    const okStyle = (okDot as HTMLElement).style.background;

    const err = render(IdentityChips, {
      props: { principal, ready: true, error: true },
    });
    const errHub = err.container.querySelector(
      '[data-testid="id-chip-hub"]',
    ) as HTMLElement | null;
    expect(errHub).not.toBeNull();
    const errDot = errHub?.querySelector(".id-chip-dot") as HTMLElement | null;
    expect(errDot).not.toBeNull();
    const errStyle = (errDot as HTMLElement).style.background;

    expect(okStyle).not.toBe(errStyle);
    // The component sets the dot via inline style: var(--ok) by default
    // and var(--danger) on error. Assert the token names directly so
    // the test fails loudly if the contract drifts off either token.
    expect(okStyle).toContain("--ok");
    expect(errStyle).toContain("--danger");
  });
});
