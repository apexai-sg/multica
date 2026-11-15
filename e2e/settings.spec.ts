import { test, expect } from "@playwright/test";
import { appRail, currentWorkspaceTile, loginAsDefault, waitForPageText } from "./helpers";

test.describe("Settings", () => {
  test("updating workspace name reflects in the rail immediately", async ({
    page,
  }) => {
    const workspaceSlug = await loginAsDefault(page);

    // Read the current workspace name from its rail tile
    const tile = currentWorkspaceTile(page);
    await expect(tile).toBeVisible({ timeout: 15000 });
    const originalName = (await tile.getAttribute("aria-label")) ?? "E2E Workspace";

    await page.goto(`/${workspaceSlug}/settings?tab=workspace`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "General");

    // Change workspace name
    const nameInput = page
      .locator('input[type="text"]')
      .first();
    await nameInput.clear();
    const newName = "Renamed WS " + Date.now();
    await nameInput.fill(newName);

    // The workspace details form auto-saves; there is no Save button. Wait for
    // the auto-save toast the same way the restore below does.
    await expect(page.getByText("Workspace settings saved").first()).toBeVisible({ timeout: 10000 });

    // The rail should reflect the new name WITHOUT page refresh
    await expect(appRail(page).getByRole("link", { name: newName })).toBeVisible();

    // Restore original name so other tests aren't affected
    await nameInput.clear();
    await nameInput.fill(originalName.trim());
    await expect(page.getByText("Workspace settings saved").first()).toBeVisible({ timeout: 10000 });
    await expect(appRail(page).getByRole("link", { name: originalName })).toBeVisible();
  });

  // Composio connect flow, fully mocked at the network boundary so it runs
  // without a configured COMPOSIO_API_KEY or a live Composio project. The
  // backend redirect is simulated by pointing the init endpoint's redirect_url
  // straight back at the settings page with ?connected=<slug> — exercising the
  // frontend's callback toast + connections refresh (MUL-3718) end to end.
  test("connecting a Composio toolkit shows a toast and refreshes the list", async ({
    page,
  }) => {
    const workspaceSlug = await loginAsDefault(page);
    const settingsUrl = `/${workspaceSlug}/settings?tab=integrations&integration=composio`;

    // Stateful: connections is empty until the (mocked) connect flow lands.
    let connected = false;

    // The Composio section is gated on the deployment's `composio_mcp_apps`
    // flag, which ships off. Patch the real config response for this spec so
    // the test does not depend on the local environment turning a product flag
    // on.
    await page.route("**/api/config", async (route) => {
      const response = await route.fetch();
      const config = (await response.json()) as {
        feature_flags?: Record<string, boolean>;
      };
      config.feature_flags = { ...config.feature_flags, composio_mcp_apps: true };
      await route.fulfill({ response, json: config });
    });

    await page.route("**/api/integrations/composio/toolkits", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([
          { slug: "notion", name: "Notion", connectable: true },
        ]),
      }),
    );

    await page.route("**/api/integrations/composio/connections", (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          connected
            ? [
                {
                  id: "conn-notion-1",
                  toolkit_slug: "notion",
                  status: "active",
                  connected_at: new Date().toISOString(),
                  last_used_at: null,
                },
              ]
            : [],
        ),
      });
    });

    await page.route("**/api/integrations/composio/connect/init", (route) => {
      // Composio would 302 through its hosted consent and back to our callback,
      // which emits CallbackRedirect's slug-less shape:
      // `/settings?tab=integrations&connected=<slug>`. The web proxy's
      // legacy-route redirect then prepends the last workspace slug, landing on
      // the real settings route. Mock that exact backend shape (NOT the final
      // slugged URL) so the test exercises the same redirect path real users hit.
      connected = true;
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          redirect_url: `/settings?tab=integrations&connected=notion`,
        }),
      });
    });

    await page.goto(settingsUrl, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "Composio");

    // Notion starts disconnected → click Connect.
    await page.getByRole("button", { name: /^Connect$/ }).first().click();

    // Success toast from the simulated callback redirect.
    await expect(page.getByText("Connected").first()).toBeVisible({ timeout: 10000 });

    // List refreshed without a manual reload: the Notion card now offers
    // Disconnect, and the one-shot ?connected param has been stripped.
    await expect(
      page.getByRole("button", { name: /Disconnect/ }).first(),
    ).toBeVisible({ timeout: 10000 });
    await expect(page).not.toHaveURL(/connected=notion/);
  });
});
