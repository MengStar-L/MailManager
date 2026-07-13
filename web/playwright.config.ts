import { defineConfig } from "@playwright/test";

const port = Number(process.env.PLAYWRIGHT_PORT ?? 4174);

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  workers: 3,
  forbidOnly: true,
  retries: 0,
  reporter: [["list"], ["html", { outputFolder: "../.artifacts/playwright-report", open: "never" }]],
  outputDir: "../.artifacts/playwright-results",
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    colorScheme: "light",
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  webServer: {
    command: `npm run dev -- --host 127.0.0.1 --port ${port}`,
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: true,
    env: { VITE_DEMO_MODE: "true" },
    timeout: 30_000,
  },
  projects: [
    { name: "desktop", use: { viewport: { width: 1440, height: 900 } } },
    { name: "tablet", use: { viewport: { width: 1024, height: 768 } } },
    { name: "mobile", use: { viewport: { width: 390, height: 844 }, reducedMotion: "reduce" } },
  ],
});
