import "./e2e/env";
import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  // The performance scenario has its own config: one worker, no retries, and a
  // running production build. It must not be swept up by the ordinary suite.
  testIgnore: "**/perf/**",
  timeout: 60000,
  workers: 1,
  retries: 0,
  use: {
    baseURL: process.env.PLAYWRIGHT_BASE_URL ?? process.env.FRONTEND_ORIGIN ?? "http://localhost:3000",
    headless: true,
  },
  projects: [
    {
      name: "chromium",
      use: { browserName: "chromium" },
      // The managed-Fleet suite only runs under its own opt-in project; the
      // default suite must never drive Docker or a Fleet environment. The perf
      // scenario stays excluded here too: a project-level testIgnore replaces
      // the top-level one rather than extending it.
      testIgnore: [
        "**/perf/**",
        "**/cloud-runtime.spec.ts",
        "**/aurora-cloud-runtime.spec.ts",
      ],
    },
    ...(process.env.MULTICA_RUN_DOCKER_INTEGRATION === "1"
      ? [
          {
            name: "fleet-docker",
            // The Aurora flow asserts on Chinese copy; pin the locale so the
            // labels do not depend on the host's language settings.
            use: { browserName: "chromium" as const, locale: "zh-CN" },
            testMatch: ["**/cloud-runtime.spec.ts", "**/aurora-cloud-runtime.spec.ts"],
          },
        ]
      : []),
  ],
  // Don't auto-start servers — they must be running already
  // This avoids complexity and port conflicts during testing
});
