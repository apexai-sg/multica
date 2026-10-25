/**
 * Aurora-on-Fleet end-to-end (Task 7, browser half).
 *
 * Gated on MULTICA_RUN_DOCKER_INTEGRATION=1 before any API or Docker access and
 * excluded from the default Playwright project, so a normal browser run never
 * talks to a Fleet host. Physical execution requires the owned managed
 * environment; a skip is not a pass.
 *
 * The behaviour under test is the browser flow: TestApiClient only signs in and
 * resolves the workspace (setup/teardown), then the expert directory opens the
 * composer, the prompt is typed and submitted in the UI, and the issue the
 * generation runs on shows the daemon's completion comment naming its artifact.
 * No API call creates or polls the generation.
 */
import { expect, test, type Page } from "@playwright/test";
import { Client } from "pg";
import { TestApiClient } from "./fixtures";
import { createTestApi, DEFAULT_E2E_EMAIL } from "./helpers";

const GENERATION_TIMEOUT_MS = 900000;
// A complete request: the ad-copywriting expert asks a clarifying question when
// the audience or deliverable is missing, and this spec is about the artifact
// path, not the question path.
const ROUND_TRIP_PROMPT =
  "为新加坡一家咖啡店写秋季新品「桂花拿铁」的中文营销文案。目标受众是 25-35 岁上班族，风格温暖、生活化。请输出 3 条小红书标题和一篇 200 字左右的正文。";
// The daemon's completion comment names every artifact it accepted.
const COMPLETION_COMMENT = /artifacts\/[a-z0-9-]+\.md/;
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

test("Aurora generation completes on a Fleet node and shows its artifact on the issue", async ({ page }) => {
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
  await drawer.getByLabel(/想要生成什么/).fill(ROUND_TRIP_PROMPT);
  await drawer.getByRole("button", { name: "开始生成", exact: true }).click();

  // A generation creates an issue and the app opens its detail page; the
  // composer closes rather than following the run.
  await page.waitForURL(/\/issues\//, { timeout: 60000 });

  // The run's result is observed on that page: the daemon posts a completion
  // comment naming the artifact it accepted, and the status pill goes away.
  // A comment is required evidence — a task that ends without an artifact has
  // nothing to show here.
  await expect(page.getByText(COMPLETION_COMMENT).first()).toBeVisible({
    timeout: GENERATION_TIMEOUT_MS,
  });
  await expect(page.getByRole("status")).toHaveCount(0);
});
