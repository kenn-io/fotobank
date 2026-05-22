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

  it("resolves /settings/ai", () => {
    setLocation("/settings/ai");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "settings.ai" });
  });

  it("resolves /settings/ai/ (trailing slash)", () => {
    setLocation("/settings/ai/");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "settings.ai" });
  });

  it("resolves /admin/settings/ai", () => {
    setLocation("/admin/settings/ai");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "admin.settings.ai" });
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

  it("parses ?from=search on /media/:id", () => {
    // Search is the fifth source kind: a search-context lightbox uses
    // ?from=search so Lightbox.svelte's fromMatchesSession can agree
    // with a snapshot.source.kind of "search".
    setLocation("/media/m1?from=search");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "media", id: "m1", from: "search" });
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

  it("matches /search with no query params", () => {
    setLocation("/search");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "search" });
  });

  it("matches /search/ (trailing slash)", () => {
    setLocation("/search/");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "search" });
  });

  it("parses /search?q=dogs", () => {
    setLocation("/search?q=dogs");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "search", q: "dogs" });
  });

  it("parses /search with sort, dates, location, media_type, include_hidden", () => {
    setLocation(
      "/search?q=trees&sort=newest&date_after=2025-01-01&date_before=2025-12-31" +
        "&location=Paris&media_type=photo&include_hidden=true",
    );
    const r = new RouterStore();
    expect(r.current).toEqual({
      route: "search",
      q: "trees",
      sort: "newest",
      date_after: "2025-01-01",
      date_before: "2025-12-31",
      location: "Paris",
      media_type: "photo",
      include_hidden: true,
    });
  });

  it("parses repeated ?tag= params into an array", () => {
    setLocation("/search?tag=cat&tag=outdoor");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "search", tag: ["cat", "outdoor"] });
  });

  it("drops unknown sort/media_type values silently", () => {
    setLocation("/search?sort=random&media_type=audio&q=a");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "search", q: "a" });
  });

  it("returns notfound for /search/extra (anchored regex)", () => {
    setLocation("/search/extra");
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

describe("RouterStore — /map query parsing", () => {
  beforeEach(() => setLocation("/"));

  it("drops empty z and c tokens (Number('') is 0, must not coerce)", () => {
    setLocation("/map?z=&c=,&focus=");
    const r = new RouterStore();
    const cur = r.current;
    expect(cur.route).toBe("map");
    if (cur.route === "map") {
      expect(cur.z).toBeUndefined();
      expect(cur.c).toBeUndefined();
      expect(cur.focus).toBeUndefined();
    }
  });

  it("drops c when only one of the two coordinates is present", () => {
    setLocation("/map?c=40.7,");
    const r = new RouterStore();
    const cur = r.current;
    expect(cur.route).toBe("map");
    if (cur.route === "map") {
      expect(cur.c).toBeUndefined();
    }
  });

  it("accepts well-formed z and c", () => {
    setLocation("/map?z=10&c=40.7,-74.0");
    const r = new RouterStore();
    const cur = r.current;
    expect(cur.route).toBe("map");
    if (cur.route === "map") {
      expect(cur.z).toBe(10);
      expect(cur.c).toEqual([40.7, -74.0]);
    }
  });
});

describe("RouterStore — from=map", () => {
  beforeEach(() => setLocation("/"));

  it("accepts from=map on the media route", () => {
    setLocation("/media/abc?from=map");
    const r = new RouterStore();
    const cur = r.current;
    expect(cur.route).toBe("media");
    if (cur.route === "media") {
      expect(cur.from).toBe("map");
    }
  });

  it("ignores garbage from values (drops the field)", () => {
    setLocation("/media/abc?from=garbage");
    const r = new RouterStore();
    const cur = r.current;
    expect(cur.route).toBe("media");
    if (cur.route === "media") {
      // Per existing parseFrom contract: unknown values fall through
      // to undefined so the field is omitted entirely.
      expect(cur.from).toBeUndefined();
    }
  });
});

describe("RouterStore — sidebar facet query params", () => {
  beforeEach(() => setLocation("/"));

  it("/library parses camera, lens, facet_tag, has_gps=1, media_type", () => {
    setLocation(
      "/library?camera=Sony+A7R+IV&camera=iPhone+15+Pro" +
        "&lens=FE+24-70mm+F2.8+GM&facet_tag=dog&has_gps=1&media_type=photo",
    );
    const r = new RouterStore();
    expect(r.current).toEqual({
      route: "library",
      camera: ["Sony A7R IV", "iPhone 15 Pro"],
      lens: ["FE 24-70mm F2.8 GM"],
      facet_tag: ["dog"],
      has_gps: true,
      media_type: "photo",
    });
  });

  it("/library with has_gps=0 carries the boolean false", () => {
    setLocation("/library?has_gps=0");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "library", has_gps: false });
  });

  it("/library accepts has_gps in 'true'/'false' form alongside the canonical '1'/'0'", () => {
    // SPA-canonical form is '1'/'0'. The backend's facets/media endpoints
    // accept 'true'/'false', so a bookmarked URL using the backend form
    // would otherwise drop the filter. Both forms (case-insensitive)
    // resolve to the same boolean.
    setLocation("/library?has_gps=true");
    expect(new RouterStore().current).toEqual({ route: "library", has_gps: true });
    setLocation("/library?has_gps=False");
    expect(new RouterStore().current).toEqual({ route: "library", has_gps: false });
  });

  it("/library drops has_gps when value is unrecognised", () => {
    setLocation("/library?has_gps=maybe");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "library" });
  });

  it("/library drops media_type when value is unknown", () => {
    setLocation("/library?media_type=audio");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "library" });
  });

  it("/library with no params remains a bare library route", () => {
    setLocation("/library");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "library" });
  });

  it("/ (root) parses sidebar facet params just like /library", () => {
    setLocation("/?camera=Sony+A7R+IV&facet_tag=dog");
    const r = new RouterStore();
    expect(r.current).toEqual({
      route: "library",
      camera: ["Sony A7R IV"],
      facet_tag: ["dog"],
    });
  });

  it("/map parses camera, lens, facet_tag, media_type but never has_gps", () => {
    setLocation(
      "/map?camera=Sony+A7R+IV&lens=FE+24-70mm+F2.8+GM" +
        "&facet_tag=cat&media_type=video&has_gps=1",
    );
    const r = new RouterStore();
    const cur = r.current;
    expect(cur).toMatchObject({
      route: "map",
      camera: ["Sony A7R IV"],
      lens: ["FE 24-70mm F2.8 GM"],
      facet_tag: ["cat"],
      media_type: "video",
    });
    expect(cur).not.toHaveProperty("has_gps");
  });

  it("/map preserves existing z, c, focus, tab alongside new facet params", () => {
    setLocation("/map?z=10&c=40.7,-74.0&tab=photos&camera=Sony+A7R+IV");
    const r = new RouterStore();
    expect(r.current).toEqual({
      route: "map",
      z: 10,
      c: [40.7, -74.0],
      tab: "photos",
      camera: ["Sony A7R IV"],
    });
  });

  it("/search merges new facet params alongside existing ones", () => {
    setLocation(
      "/search?q=mountain&tag=Dog&camera=Sony+A7R+IV" +
        "&facet_tag=cat&has_gps=0",
    );
    const r = new RouterStore();
    expect(r.current).toEqual({
      route: "search",
      q: "mountain",
      tag: ["Dog"],
      camera: ["Sony A7R IV"],
      facet_tag: ["cat"],
      has_gps: false,
    });
  });

  it("/search with has_gps=1 carries the boolean true", () => {
    setLocation("/search?has_gps=1");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "search", has_gps: true });
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
