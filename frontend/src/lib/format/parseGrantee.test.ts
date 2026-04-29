import { describe, it, expect } from "vitest";
import { parseGrantee } from "./parseGrantee";

describe("parseGrantee", () => {
  it("parses valid hub:user_id", () => {
    expect(parseGrantee("myhub:bob")).toEqual({ hub: "myhub", user_id: "bob" });
  });
  it("trims whitespace", () => {
    expect(parseGrantee("  myhub:bob  ")).toEqual({ hub: "myhub", user_id: "bob" });
  });
  it("rejects missing colon", () => {
    expect(parseGrantee("myhub")).toBeNull();
  });
  it("rejects empty hub", () => {
    expect(parseGrantee(":bob")).toBeNull();
  });
  it("rejects empty user_id", () => {
    expect(parseGrantee("myhub:")).toBeNull();
  });
  it("rejects internal whitespace", () => {
    expect(parseGrantee("my hub:bob")).toBeNull();
  });
  it("rejects multiple colons", () => {
    expect(parseGrantee("hub:user:id")).toBeNull();
  });
  it("rejects oversized fields", () => {
    expect(parseGrantee("a".repeat(300) + ":bob")).toBeNull();
    expect(parseGrantee("hub:" + "b".repeat(300))).toBeNull();
  });
});
