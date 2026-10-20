import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import type { TestProject } from "vitest/node";

declare module "vitest" {
  export interface ProvidedContext {
    securityFixtureBinary: string;
  }
}

/** Build once per run, before workers start, rather than racing seven Go
 * compilers and charging compilation to whichever test first uses a fixture.
 * Only this immutable executable is shared; each fixture has its own process. */
export default function setup(project: TestProject) {
  const directory = mkdtempSync(join(tmpdir(), "ra-web-security-"));
  try {
    const binary = join(directory, "fixture");
    execFileSync("go", ["build", "-o", binary, "./internal/testutil/securityfixture"], {
      cwd: resolve(fileURLToPath(new URL("../../..", import.meta.url))),
      stdio: "pipe",
    });
    project.provide("securityFixtureBinary", binary);
  } catch (error) {
    rmSync(directory, { recursive: true, force: true });
    throw error;
  }
  return () => rmSync(directory, { recursive: true, force: true });
}
