import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";
import { RouterStore, handleInternalLinkClick } from "./router.svelte";

function setLocation(pathname: string) {
  window.history.replaceState({}, "", pathname);
}

describe("RouterStore.match", () => {
  beforeEach(() => setLocation("/"));

  it("matches /, /library, /sessions, /settings", () => {
    const r = new RouterStore();
    setLocation("/"); r.syncFromLocation();
    expect(r.current.route).toBe("library");
    setLocation("/library"); r.syncFromLocation();
    expect(r.current.route).toBe("library");
    setLocation("/sessions"); r.syncFromLocation();
    expect(r.current.route).toBe("sessions");
    setLocation("/settings"); r.syncFromLocation();
    expect(r.current.route).toBe("settings");
  });

  it("matches /media/:id and exposes the id", () => {
    setLocation("/media/abc");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "media", id: "abc" });
  });

  it("ignores search and hash when matching", () => {
    setLocation("/media/abc?return=library");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "media", id: "abc" });
  });

  it("parses ?from=library on /media/:id", () => {
    setLocation("/media/abc?from=library");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "media", id: "abc", from: "library" });
  });

  it("parses ?from=sessions / ?from=hidden on /media/:id", () => {
    setLocation("/media/x?from=sessions");
    let r = new RouterStore();
    expect(r.current).toEqual({ route: "media", id: "x", from: "sessions" });
    setLocation("/media/y?from=hidden");
    r = new RouterStore();
    expect(r.current).toEqual({ route: "media", id: "y", from: "hidden" });
  });

  it("parses ?from=album:abc on /media/:id", () => {
    setLocation("/media/abc?from=album:my-album-123");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "media", id: "abc", from: "album:my-album-123" });
  });

  it("/media/:id without ?from has no from field", () => {
    setLocation("/media/abc");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "media", id: "abc" });
  });

  it("ignores unknown ?from values (treats as absent)", () => {
    setLocation("/media/abc?from=nope");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "media", id: "abc" });
  });

  it("returns notfound for unknown pathnames (with or without query)", () => {
    setLocation("/foo");
    let r = new RouterStore();
    expect(r.current.route).toBe("notfound");
    setLocation("/foo?bar=baz");
    r = new RouterStore();
    expect(r.current.route).toBe("notfound");
  });

  it("returns notfound for /media/abc/extra (anchored regex)", () => {
    setLocation("/media/abc/extra");
    const r = new RouterStore();
    expect(r.current.route).toBe("notfound");
  });

  it("matches /albums to the albums route", () => {
    setLocation("/albums");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "albums" });
  });

  it("matches /albums/ (trailing slash) to the albums route", () => {
    setLocation("/albums/");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "albums" });
  });

  it("matches /albums/abc-123 to albums.detail with the id", () => {
    setLocation("/albums/abc-123");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "albums.detail", id: "abc-123" });
  });

  it("returns notfound for /albums/abc/extra (anchored regex)", () => {
    setLocation("/albums/abc/extra");
    const r = new RouterStore();
    expect(r.current.route).toBe("notfound");
  });

  it("matches /shares to the shares route", () => {
    setLocation("/shares");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "shares" });
  });

  it("matches /shares/ (trailing slash) to the shares route", () => {
    setLocation("/shares/");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "shares" });
  });

  it("parses ?album_id from /shares", () => {
    setLocation("/shares?album_id=abc-123");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "shares", album_id: "abc-123" });
  });

  it("parses ?show_revoked=true from /shares", () => {
    setLocation("/shares?show_revoked=true");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "shares", show_revoked: true });
  });

  it("parses both ?album_id and ?show_revoked from /shares", () => {
    setLocation("/shares?album_id=abc&show_revoked=true");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "shares", album_id: "abc", show_revoked: true });
  });

  it("matches /hidden to the hidden route", () => {
    setLocation("/hidden");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "hidden" });
  });

  it("matches /hidden/ (trailing slash) to the hidden route", () => {
    setLocation("/hidden/");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "hidden" });
  });

  it("returns notfound for /hidden/extra (anchored regex)", () => {
    setLocation("/hidden/extra");
    const r = new RouterStore();
    expect(r.current.route).toBe("notfound");
  });
});

describe("RouterStore.navigate", () => {
  beforeEach(() => setLocation("/"));

  it("pushes history and updates current", () => {
    const r = new RouterStore();
    r.navigate("/sessions");
    expect(window.location.pathname).toBe("/sessions");
    expect(r.current.route).toBe("sessions");
  });

  it("syncFromLocation re-reads window.location", () => {
    const r = new RouterStore();
    setLocation("/library");
    r.syncFromLocation();
    expect(r.current.route).toBe("library");
  });

  it("increments appHistoryDepth on each non-replace navigate", () => {
    const r = new RouterStore();
    r.navigate("/library");
    r.navigate("/sessions");
    r.navigate("/hidden");
    // depth is private — test it indirectly via back() calling history.back
    const backSpy = vi.spyOn(window.history, "back").mockReturnValue(undefined);
    r.back("/library");
    expect(backSpy).toHaveBeenCalled();
    backSpy.mockRestore();
  });

  it("replace:true does not increment appHistoryDepth", () => {
    const r = new RouterStore();
    r.navigate("/library", { replace: true });
    const backSpy = vi.spyOn(window.history, "back").mockReturnValue(undefined);
    // depth is still 0 so back() should navigate to fallback, not call history.back
    r.back("/sessions");
    expect(backSpy).not.toHaveBeenCalled();
    expect(window.location.pathname).toBe("/sessions");
    backSpy.mockRestore();
  });
});

describe("RouterStore.back", () => {
  beforeEach(() => setLocation("/"));
  afterEach(() => vi.restoreAllMocks());

  it("calls history.back when appHistoryDepth > 0", () => {
    const r = new RouterStore();
    r.navigate("/sessions");
    const backSpy = vi.spyOn(window.history, "back").mockReturnValue(undefined);
    r.back("/library");
    expect(backSpy).toHaveBeenCalledOnce();
  });

  it("navigates to fallback when appHistoryDepth === 0", () => {
    setLocation("/hidden");
    const r = new RouterStore();
    // No in-app navigate calls → depth stays 0.
    const backSpy = vi.spyOn(window.history, "back").mockReturnValue(undefined);
    r.back("/library");
    expect(backSpy).not.toHaveBeenCalled();
    expect(window.location.pathname).toBe("/library");
    expect(r.current.route).toBe("library");
  });

  it("decrements depth after each back call", () => {
    const r = new RouterStore();
    r.navigate("/library");
    r.navigate("/sessions");
    const backSpy = vi.spyOn(window.history, "back").mockReturnValue(undefined);
    r.back("/library");
    expect(backSpy).toHaveBeenCalledOnce();
    backSpy.mockClear();
    // depth now 1 → still calls history.back
    r.back("/library");
    expect(backSpy).toHaveBeenCalledOnce();
    backSpy.mockClear();
    // depth now 0 → fallback
    r.back("/library");
    expect(backSpy).not.toHaveBeenCalled();
  });
});

describe("RouterStore.syncFromLocation depth restore (finding #12)", () => {
  beforeEach(() => setLocation("/"));

  it("restores appHistoryDepth from history.state on syncFromLocation", () => {
    const r = new RouterStore();
    r.navigate("/library");
    r.navigate("/sessions");
    // Simulate browser back/forward: directly set history.state with a depth
    window.history.replaceState({ __fotobank_depth__: 1 }, "", "/library");
    r.syncFromLocation();
    // After restoring depth=1, back() should call history.back, not fallback
    const backSpy = vi.spyOn(window.history, "back").mockReturnValue(undefined);
    r.back("/notfound");
    expect(backSpy).toHaveBeenCalledOnce();
    backSpy.mockRestore();
  });

  it("depth=0 in history.state causes back() to navigate to fallback", () => {
    const r = new RouterStore();
    window.history.replaceState({ __fotobank_depth__: 0 }, "", "/library");
    r.syncFromLocation();
    const backSpy = vi.spyOn(window.history, "back").mockReturnValue(undefined);
    r.back("/sessions");
    expect(backSpy).not.toHaveBeenCalled();
    expect(window.location.pathname).toBe("/sessions");
    backSpy.mockRestore();
  });
});

describe("handleInternalLinkClick", () => {
  it("preventDefaults and navigates on plain left click", () => {
    setLocation("/");
    const r = new RouterStore();
    const e = new MouseEvent("click", { button: 0, cancelable: true });
    handleInternalLinkClick(e, "/sessions", r);
    expect(e.defaultPrevented).toBe(true);
    expect(r.current.route).toBe("sessions");
  });

  it("ignores middle-click, right-click, and modifier keys", () => {
    setLocation("/");
    const r = new RouterStore();
    for (const init of [
      { button: 1 },
      { button: 2 },
      { button: 0, metaKey: true },
      { button: 0, ctrlKey: true },
      { button: 0, shiftKey: true },
      { button: 0, altKey: true },
    ]) {
      const e = new MouseEvent("click", { ...init, cancelable: true });
      handleInternalLinkClick(e, "/sessions", r);
      expect(e.defaultPrevented).toBe(false);
    }
  });
});
