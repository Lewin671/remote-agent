import { availableParallelism } from "node:os";
import { configDefaults, defineConfig, mergeConfig } from "vitest/config";
import viteConfig from "./vite.config.ts";

export default mergeConfig(viteConfig, defineConfig({
  test: {
    // Crypto fixtures launch Go processes too. Leave CPU capacity for them and
    // other suites instead of starting one JS worker per reported core.
    maxWorkers: Math.min(4, availableParallelism()),
    projects: [
      {
        extends: true,
        test: {
          name: "web",
          exclude: [...configDefaults.exclude, "src/markdown.complexity.test.ts"],
          globalSetup: ["./src/test/global-setup.ts"],
        },
      },
      {
        extends: true,
        test: {
          name: "markdown-complexity",
          include: ["src/markdown.complexity.test.ts"],
          // V8's optimizing tiers can inline helpers and omit their invocation
          // counts even with precise coverage enabled. Keep work units stable
          // in this worker; all other tests still exercise the normal JIT.
          execArgv: ["--max-opt=1"],
        },
      },
    ],
  },
}));
