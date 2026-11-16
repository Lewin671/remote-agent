import { describe, expect, it } from "vitest";
import { DEFAULT_EXTRA_STUN, stunServers } from "./direct-engine";

describe("stunServers", () => {
  it("uses the relay's own server and keeps Cloudflare, by name and by IPv6 address, for the last resort", () => {
    expect(stunServers("ra.example.com")).toEqual({ own: "stun:ra.example.com:3478", extra: DEFAULT_EXTRA_STUN });
    expect(DEFAULT_EXTRA_STUN.some((u) => u.includes("[2606:"))).toBe(true);
  });
  it("brackets an IPv6 relay host", () => {
    expect(stunServers("2001:db8::1").own).toBe("stun:[2001:db8::1]:3478");
  });
  it("can leave the public servers out or replace them", () => {
    expect(stunServers("ra.example.com", undefined, "none").extra).toEqual([]);
    expect(stunServers("ra.example.com", "stun:own.example:3478", "stun:a.example:3478, stun:b.example:3478")).toEqual({ own: "stun:own.example:3478", extra: ["stun:a.example:3478", "stun:b.example:3478"] });
  });
  it("does not list the own server twice", () => {
    expect(stunServers("x", DEFAULT_EXTRA_STUN[0]).extra).toEqual(DEFAULT_EXTRA_STUN.slice(1));
  });
});
