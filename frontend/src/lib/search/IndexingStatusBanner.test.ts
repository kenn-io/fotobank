import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import { flushSync, tick } from "svelte";
import IndexingStatusBanner from "./IndexingStatusBanner.svelte";

// IndexingStatusBanner is the search-page banner that explains why the
// engine fell back to BM25. The component surfaces three orthogonal
// banners, evaluated in priority order via the {#if/:else if} chain:
//
//   1. under-80% — a "still indexing" hint shown only while the user
//      has typed a query and the library hasn't reached the activator
//      threshold (semanticUnavailable=false; the engine is hybrid-
//      ranking what it has). Suppressed once semanticUnavailable=true
//      so the more specific banners take precedence.
//   2. no_active_generation — semantic search has never been activated
//      for this library. Dismissable per browser-tab session via
//      sessionStorage so a /search ↔ other-route navigation cycle
//      preserves the dismissal. Tab close clears sessionStorage,
//      surfacing the banner again on the next session.
//   3. query_embedding_failed — the engine had an active generation
//      but the per-query embedding call failed for the latest request.
//      Auto-dismisses on the next successful query because `reason`
//      transitions back to "" — there is no per-component dismiss
//      state to reset.

describe("IndexingStatusBanner", () => {
  // Per-test isolation: the no_active_generation dismissal persists in
  // sessionStorage. Without a clear, the DismissalPersistsAcrossRemount
  // test would leak into the "is dismissable per session" test (which
  // expects the banner to render initially).
  beforeEach(() => {
    if (typeof sessionStorage !== "undefined") {
      sessionStorage.clear();
    }
  });
  it("under-80% banner shows when q != '' && completeness < 0.80 && !semanticUnavailable", () => {
    // Plan acceptance: the 80% threshold matches the activator's
    // EmbeddingThreshold (the search engine's hybrid pivot). Below it,
    // hybrid ranking covers a partial library — the banner surfaces
    // that to the user so they understand why some results "feel"
    // weaker. The banner is only relevant while the user has a query;
    // an empty-query view doesn't need the explanation.
    const { container, getByText } = render(IndexingStatusBanner, {
      props: {
        completeness: 0.5,
        semanticUnavailable: false,
        reason: "",
        hasQuery: true,
      },
    });
    expect(container.querySelector(".banner")).not.toBeNull();
    // Body mentions the rounded percentage so the user has a concrete
    // sense of how much of the library is covered.
    expect(getByText(/50%/)).toBeTruthy();
  });

  it("under-80% banner does NOT show when query is empty", () => {
    // Without a query the banner is noise — there's nothing being
    // ranked. Tests the (hasQuery=false) branch of the showUnderEighty
    // $derived guard.
    const { container } = render(IndexingStatusBanner, {
      props: {
        completeness: 0.5,
        semanticUnavailable: false,
        reason: "",
        hasQuery: false,
      },
    });
    expect(container.querySelector(".banner")).toBeNull();
  });

  it("under-80% banner does NOT show when semanticUnavailable=true", () => {
    // When semantic is fully unavailable the no_active_generation /
    // query_embedding_failed banners take over. The under-80% banner's
    // !semanticUnavailable guard ensures the more specific message
    // wins.
    const { container } = render(IndexingStatusBanner, {
      props: {
        completeness: 0.5,
        semanticUnavailable: true,
        reason: "",
        hasQuery: true,
      },
    });
    expect(container.querySelector(".banner")).toBeNull();
  });

  it("no_active_generation banner is dismissable per session", async () => {
    // The dismiss button flips local `dismissed` $state which gates
    // the banner. After clicking, the banner disappears and stays
    // dismissed for the lifetime of the component. Plan calls this
    // "per-session" because remounting the component resets the
    // local state — the user gets a fresh banner if they navigate
    // away and back, but not while the page is mounted.
    const { container, getByText } = render(IndexingStatusBanner, {
      props: {
        completeness: 0,
        semanticUnavailable: true,
        reason: "no_active_generation",
        hasQuery: false,
      },
    });
    expect(container.querySelector(".banner")).not.toBeNull();
    const dismiss = getByText("Dismiss");
    await fireEvent.click(dismiss);
    flushSync();
    await tick();
    expect(container.querySelector(".banner")).toBeNull();
  });

  it("DismissalPersistsAcrossRemount", async () => {
    // Plan acceptance: the no_active_generation Dismiss is scoped to
    // the browser-tab session, not the component lifetime. Navigating
    // away from /search and back (which remounts the component) must
    // preserve the dismissal. This test simulates that by mounting,
    // dismissing, unmounting, then remounting with the same reason —
    // the second mount must hydrate `dismissed=true` from
    // sessionStorage and skip the banner.
    const first = render(IndexingStatusBanner, {
      props: {
        completeness: 0,
        semanticUnavailable: true,
        reason: "no_active_generation",
        hasQuery: false,
      },
    });
    expect(first.container.querySelector(".banner")).not.toBeNull();
    await fireEvent.click(first.getByText("Dismiss"));
    flushSync();
    await tick();
    expect(first.container.querySelector(".banner")).toBeNull();
    // sessionStorage should now hold the dismissal flag.
    expect(
      sessionStorage.getItem("fotobank.search.banner.no_active_generation"),
    ).toBe("true");
    // Tear down the first instance (Svelte route navigation away).
    first.unmount();

    // Remount with the same reason — emulates returning to /search in
    // the same tab. The component must hydrate dismissed=true from
    // sessionStorage and stay collapsed.
    const second = render(IndexingStatusBanner, {
      props: {
        completeness: 0,
        semanticUnavailable: true,
        reason: "no_active_generation",
        hasQuery: false,
      },
    });
    expect(second.container.querySelector(".banner")).toBeNull();
  });

  it("HandlesThrowingStorage", async () => {
    // Browsers can expose Storage but throw SecurityError on access
    // (private mode quirks, third-party-cookie blocking, sandboxed
    // iframes). The component's try/catch wrappers must swallow those
    // throws so the banner still renders normally — the dismissal is
    // a non-critical preference. Mock both getItem and setItem to
    // throw; assert the banner renders (not dismissed) and clicking
    // Dismiss completes without error and still hides the banner via
    // local component state.
    const getSpy = vi
      .spyOn(Storage.prototype, "getItem")
      .mockImplementation(() => {
        throw new DOMException("blocked", "SecurityError");
      });
    const setSpy = vi
      .spyOn(Storage.prototype, "setItem")
      .mockImplementation(() => {
        throw new DOMException("blocked", "SecurityError");
      });
    try {
      const { container, getByText } = render(IndexingStatusBanner, {
        props: {
          completeness: 0,
          semanticUnavailable: true,
          reason: "no_active_generation",
          hasQuery: false,
        },
      });
      // Banner renders despite the throwing getItem. The hydration
      // path treats the read as null (no prior dismissal), and the
      // showNoGen $derived gates the banner on `!dismissed` which
      // is therefore false → banner shown.
      expect(container.querySelector(".banner")).not.toBeNull();
      expect(getSpy).toHaveBeenCalled();

      // Clicking Dismiss must not crash even though setItem throws.
      // Local `dismissed` $state still flips, so the banner is hidden
      // for the lifetime of this mount.
      await fireEvent.click(getByText("Dismiss"));
      flushSync();
      await tick();
      expect(container.querySelector(".banner")).toBeNull();
      expect(setSpy).toHaveBeenCalled();
    } finally {
      getSpy.mockRestore();
      setSpy.mockRestore();
    }
  });

  it("query_embedding_failed banner auto-dismisses when reason transitions back to ''", async () => {
    // The banner has no per-component dismiss for this branch — the
    // showQueryEmbedFailed $derived only renders when reason is
    // "query_embedding_failed". The next successful query response
    // carries reason="", which flips the $derived false and the banner
    // disappears. The test patches the props via $set to drive the
    // transition.
    const { container, rerender } = render(IndexingStatusBanner, {
      props: {
        completeness: 1,
        semanticUnavailable: true,
        reason: "query_embedding_failed",
        hasQuery: true,
      },
    });
    expect(container.querySelector(".banner")).not.toBeNull();
    await rerender({
      completeness: 1,
      semanticUnavailable: false,
      reason: "",
      hasQuery: true,
    });
    flushSync();
    await tick();
    expect(container.querySelector(".banner")).toBeNull();
  });
});
