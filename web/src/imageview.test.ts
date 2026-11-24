import { describe, expect, it } from "vitest";
import { ImageFormat } from "./gen/remoteagent/v1/event_pb";
import { matchesFormat } from "./imageview";

describe("format checks", () => {
  it("accepts only bytes that are the claimed format", () => {
    const png = Uint8Array.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0]);
    const jpeg = Uint8Array.from([0xff, 0xd8, 0xff, 0xe0]);
    const webp = Uint8Array.from([0x52, 0x49, 0x46, 0x46, 0, 0, 0, 0, 0x57, 0x45, 0x42, 0x50]);
    const gif = new TextEncoder().encode("GIF89a");
    const svg = new TextEncoder().encode("<svg onload=alert(1)>");
    expect(matchesFormat(png, ImageFormat.PNG)).toBe(true);
    expect(matchesFormat(jpeg, ImageFormat.JPEG)).toBe(true);
    expect(matchesFormat(webp, ImageFormat.WEBP)).toBe(true);
    expect(matchesFormat(gif, ImageFormat.GIF)).toBe(true);
    expect(matchesFormat(jpeg, ImageFormat.PNG)).toBe(false);
    expect(matchesFormat(svg, ImageFormat.PNG)).toBe(false);
    expect(matchesFormat(svg, ImageFormat.UNSPECIFIED)).toBe(false);
  });
});
