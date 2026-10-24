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
import { Client } from "pg";
import { TestApiClient } from "./fixtures";
import { createTestApi, DEFAULT_E2E_EMAIL } from "./helpers";

const GENERATION_TIMEOUT_MS = 900000;
const ROUND_TRIP_PROMPT = "aurora e2e fleet round trip poster";
// A current catalog expert (marketing-ad-copywriting), which the node image
// carries. The fleet project pins locale zh-CN so these labels are stable.
const EXPERT_NAME = "广告文案专家";

// The Aurora app is a separate origin from the default Playwright baseURL. It
// is read from the environment when the managed composition names it, and
// otherwise follows the checkout's AURORA_PORT default.
const AURORA_BASE_URL = (
  process.env.MULTICA_AURORA_BASE_URL ??
  process.env.AURORA_ORIGIN ??
  `http://localhost:${process.env.AURORA_PORT?.trim() || "13011"}`
).replace(/\/+$/, "");

let api: TestApiClient | null = null;

// The API origin the browser talks to. Aurora proxies /api /auth /ws to it, so
// a cookie minted against this origin is sent to the app's own origin too.
const API_BASE = process.env.NEXT_PUBLIC_API_URL ?? `http://localhost:${process.env.PORT || "8080"}`;
const DATABASE_URL =
  process.env.DATABASE_URL ?? "postgres://multica:multica@localhost:5432/multica?sslmode=disable";

/**
 * Aurora authenticates with an HttpOnly cookie, so the session has to be
 * created inside the browser context. A localStorage token — enough for the web
 * app's legacy path — is ignored by Aurora and leaves the page on the sign-in
 * screen.
 */
async function loginFleetBrowser(page: Page, client: TestApiClient): Promise<void> {
  if (!client.getToken()) throw new Error("aurora E2E client is not logged in");
  const db = new Client({ connectionString: DATABASE_URL });
  await db.connect();
  try {
    await db.query("DELETE FROM verification_code WHERE email = $1", [DEFAULT_E2E_EMAIL]);
    const send = await page.request.post(`${API_BASE}/auth/send-code`, {
      data: { email: DEFAULT_E2E_EMAIL },
    });
    if (!send.ok()) throw new Error(`send-code failed: ${send.status()}`);
    const result = await db.query(
      "SELECT code FROM verification_code WHERE email = $1 AND used = FALSE ORDER BY created_at DESC LIMIT 1",
      [DEFAULT_E2E_EMAIL],
    );
    if (result.rows.length === 0) throw new Error("no verification code");
    const verify = await page.request.post(`${API_BASE}/auth/verify-code`, {
      data: { email: DEFAULT_E2E_EMAIL, code: result.rows[0].code },
    });
    if (!verify.ok()) throw new Error(`verify-code failed: ${verify.status()}`);
  } finally {
    await db.end();
  }
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

  // Setup only: land on the expert directory for the resolved workspace. The
  // directory is `/skills/connected`; `/skills` is the static tool grid.
  await page.goto(
    `${AURORA_BASE_URL}/${client.getFleetWorkspaceSlug()}/skills/connected`,
    { waitUntil: "domcontentloaded" },
  );

  // The prompt goes through the composer UI. Picking the expert opens the drawer.
  await page.getByRole("button", { name: new RegExp(EXPERT_NAME) }).click();
  const drawer = page.locator('[data-slot="sheet-content"]');
  await expect(drawer).toBeVisible();
  await drawer.getByLabel("想要生成什么？").fill(ROUND_TRIP_PROMPT);
  await drawer.getByRole("button", { name: "开始生成", exact: true }).click();

  // The drawer follows the generation it started and renders the terminal
  // status itself. This is a browser assertion, not an API poll.
  await expect(drawer.getByRole("status")).toHaveText("完成", {
    timeout: GENERATION_TIMEOUT_MS,
  });

  // The artifact is observed in the UI: the composer's Result section lists the
  // asset's download link. A status-only check would also pass a completed
  // generation that produced no files, so the link is required.
  await expect(drawer.getByRole("heading", { name: "生成结果" })).toBeVisible();
  await expect(drawer.locator('ul li a[href$="/download"]').first()).toBeVisible();
});
