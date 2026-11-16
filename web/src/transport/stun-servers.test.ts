import { describe, expect, it } from "vitest";
import { stunServers } from "./direct-engine";

describe("stunServers", () => {
  it("falls back to the relay's own STUN", () => {
    expect(stunServers("ra.example.com")).toEqual(["stun:ra.example.com:3478"]);
  });
  it("brackets an IPv6 relay host", () => {
    expect(stunServers("2001:db8::1")).toEqual(["stun:[2001:db8::1]:3478"]);
  });
  it("takes a deployment's own list, so one entry can be an IPv6 literal", () => {
    expect(stunServers("ra.example.com", "stun:stun.example.com:3478, stun:[2001:db8::2]:3478")).toEqual(["stun:stun.example.com:3478", "stun:[2001:db8::2]:3478"]);
  });
  it("ignores blank entries and keeps at most two", () => {
    expect(stunServers("ra.example.com", " , ")).toEqual(["stun:ra.example.com:3478"]);
    expect(stunServers("ra.example.com", "stun:a:3478,stun:b:3478,stun:c:3478").length).toBe(2);
  });
});
