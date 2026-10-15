import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  testMatch: "rendered-journey.spec.mjs",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 240000,
  expect: { timeout: 10000 },
  reporter: "line",
  outputDir: "../../.batuta/runs/rendered-browser/results",
  use: { browserName: "chromium", trace: "off", screenshot: "off", video: "off" },
  projects: [{ name: "chromium" }],
});
