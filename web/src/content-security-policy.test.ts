import { expect, it } from "vitest";
import { contentSecurityPolicy } from "./content-security-policy";

it("allows local audio/video blobs while keeping executable content restricted", () => {
  const policy = contentSecurityPolicy("test-theme-hash");
  expect(policy).toContain("media-src 'self' blob:");
  expect(policy).toContain("default-src 'self'");
  expect(policy).toContain("object-src 'none'");
  expect(policy).toContain("script-src 'self' 'sha256-test-theme-hash'");
});
