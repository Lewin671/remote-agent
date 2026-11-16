import { describe, expect, it } from "vitest";
import { DEFAULT_EXTRA_STUN, stunServers } from "./direct-engine";

describe("stunServers", () => {
  it("adds a dual-stack public STUN after the relay's own one", () => {
    expect(stunServers("ra.example.com")).toEqual(["stun:ra.example.com:3478", DEFAULT_EXTRA_STUN]);
  });
  it("brackets an IPv6 relay host", () => {
    expect(stunServers("2001:db8::1")[0]).toBe("stun:[2001:db8::1]:3478");
  });
  it("can drop or replace the extra server", () => {
    expect(stunServers("ra.example.com", undefined, "none")).toEqual(["stun:ra.example.com:3478"]);
    expect(stunServers("ra.example.com", "stun:own.example:3478", "stun:other.example:3478")).toEqual(["stun:own.example:3478", "stun:other.example:3478"]);
  });
  it("never lists the same server twice", () => {
    expect(stunServers("x", DEFAULT_EXTRA_STUN)).toEqual([DEFAULT_EXTRA_STUN]);
  });
});
