// Browser tests of the local viewer's built page (web/viewer/e2e). Build it
// first: CERNLY_VIEWER_TEST_PLUGINS=1 web/viewer/build.sh
import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "viewer/e2e",
  globalSetup: "./viewer/e2e/setup.mjs",
  timeout: 120_000,
  reporter: process.env.CI ? "list" : "line",
  use: { acceptDownloads: true },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "firefox", use: { ...devices["Desktop Firefox"] } },
    { name: "webkit", use: { ...devices["Desktop Safari"] } },
  ],
});
