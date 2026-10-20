import { configDefaults, defineConfig, mergeConfig } from "vitest/config";
import viteConfig from "./vite.config.ts";

export default mergeConfig(viteConfig, defineConfig({
  test: {
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
