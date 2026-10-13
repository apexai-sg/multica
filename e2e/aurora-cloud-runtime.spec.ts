/**
 * Aurora-on-Fleet end-to-end (Task 7, browser half).
 *
 * Gated on MULTICA_RUN_DOCKER_INTEGRATION=1 before any API or Docker access and
 * excluded from the default Playwright project, so a normal browser run never
 * talks to a Fleet host. Physical execution requires the owned managed
 * environment; a skip is not a pass.
 *
 * The behaviour under test is the browser flow: TestApiClient only signs in and
 * resolves the workspace (setup/teardown), then the Aurora Create directory
 * opens the composer, the prompt is typed and submitted in the UI, and the
 * generation's rendered status badge and artifact link are the evidence. No API
 * call creates or polls the generation. The same flow stays a real-engine skip
 * until the managed environment carries a fake-capable dual-contract node image
 * (the Go half names that prerequisite in aurora_test.go).
 */
import { expect, test, type Page } from "@playwright/test";
import { TestApiClient } from "./fixtures";
import { createTestApi } from "./helpers";

const GENERATION_TIMEOUT_MS = 900000;
const ROUND_TRIP_PROMPT = "aurora e2e fleet round trip poster";
// The catalog's English name for the xhs-image skill, the same skill the gated
// Go round trip submits. The default locale is en.
const SKILL_NAME = "Xiaohongshu Image";

// The Aurora app is a separate origin from the default Playwright baseURL. It
// is read from the environment when the managed composition names it, and
// otherwise follows the checkout's AURORA_PORT default.
const AURORA_BASE_URL = (
  process.env.MULTICA_AURORA_BASE_URL ??
  process.env.AURORA_ORIGIN ??
  `http://localhost:${process.env.AURORA_PORT?.trim() || "13011"}`
).replace(/\/+$/, "");

let api: TestApiClient | null = null;

async function loginFleetBrowser(page: Page, client: TestApiClient): Promise<void> {
  const token = client.getToken();
  if (!token) throw new Error("aurora E2E client is not logged in");
  await page.addInitScript((value) => {
    localStorage.setItem("multica_token", value);
    localStorage.setItem("multica:chat:isOpen", "false");
  }, token);
}

test.afterEach(async ({}, testInfo) => {
  // The managed node and the generation it backed are owned by the server
  // reaper; the client cleanup removes only the SQL-owner fixtures this spec
  // created. Nothing is pruned.
  testInfo.setTimeout(180000);
  const client = api;
  api = null;
  await client?.cleanup().catch(() => {});
});

test("Aurora generation completes on a Fleet node and shows its artifact in the composer", async ({ page }) => {
  test.skip(process.env.MULTICA_RUN_DOCKER_INTEGRATION !== "1");
  const client = await createTestApi();
  api = client;
  await loginFleetBrowser(page, client);

  // Setup only: land on the Aurora Create directory for the resolved workspace.
  await page.goto(`${AURORA_BASE_URL}/${client.getFleetWorkspaceSlug()}/skills`, {
    waitUntil: "domcontentloaded",
  });

  // The prompt goes through the composer UI. Picking the skill opens the drawer.
  await page.getByRole("button", { name: SKILL_NAME }).click();
  const drawer = page.locator('[data-slot="sheet-content"]');
  await expect(drawer).toBeVisible();
  await drawer.getByLabel("What should it make?").fill(ROUND_TRIP_PROMPT);
  await drawer.getByRole("button", { name: "Generate", exact: true }).click();

  // The drawer follows the generation it started and renders the terminal
  // status itself. This is a browser assertion, not an API poll.
  await expect(drawer.getByRole("status")).toHaveText("Done", {
    timeout: GENERATION_TIMEOUT_MS,
  });

  // The artifact is observed in the UI: the composer's Result section lists the
  // asset's download link. A status-only check would also pass a completed
  // generation that produced no files, so the link is required.
  await expect(drawer.getByRole("heading", { name: "Result" })).toBeVisible();
  await expect(drawer.locator('ul li a[href$="/download"]').first()).toBeVisible();
});
