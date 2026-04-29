import { describe, it, expect, beforeEach } from "vitest";
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
