import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { renderWithI18n } from "../test/i18n";

// The view resolves its own workspace and drives the real query/parser stack,
// so the test replaces the API client at @multica/core/api — the seam the
// production client is installed through — rather than mocking the hooks. No
// next/* or react-router-dom module is mocked: the component does not reach a
// router at all, which is part of what this suite pins.
vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-1",
}));

import { AuroraRuntimeChip, RuntimeStatus } from "./runtime-status";
import { NavigationProvider } from "../navigation";
import { stubNavigationAdapter } from "../test/navigation";

type RuntimeBody = unknown;

type RouteHandler = (path: string) => unknown;

/** Installs a read-only API client that routes by path. */
function installApi(handler: RouteHandler) {
  setApiInstance({
    requestJson: async (path: string, options?: RequestInit) => {
      expect(options?.method ?? "GET").toBe("GET");
      return handler(path);
    },
  } as unknown as ApiClient);
}

const TARGET: RuntimeBody = {
  workspaceId: "ws-1",
  node: {
    id: "node-1",
    status: "online",
    ready: true,
    provider: "docker",
    createdAt: "2026-10-06T00:00:00Z",
  },
  runtimeId: "rt-1",
  state: "online",
};

function generation(overrides: Record<string, unknown> = {}) {
  return {
    id: "gen-1",
    skillId: "poster",
    prompt: "a launch poster",
    status: "running",
    creditsReserved: 76,
    ...overrides,
  };
}

/**
 * Routes the two endpoints the view reads. `runtimeBody` may be a non-object,
 * which is how the malformed-response case is exercised.
 */
function installReads(
  runtimeBody: RuntimeBody,
  generations: unknown[] = [],
) {
  installApi((path) => {
    if (path === "/api/aurora/runtime") return runtimeBody;
    if (path === "/api/aurora/generations") return { generations };
    if (path === "/api/aurora/generations/gen-1") {
      return { generation: generation({ assets: [] }) };
    }
    throw new Error(`unexpected path ${path}`);
  });
}

function renderRuntime() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <RuntimeStatus />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  installReads(TARGET);
});

afterEach(() => {
  cleanup();
  setApiInstance(null as unknown as ApiClient);
});

describe("AuroraRuntimeChip", () => {
  function renderChip() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderWithI18n(<QueryClientProvider client={client}>
      <NavigationProvider value={stubNavigationAdapter()}>
        <AuroraRuntimeChip href="/acme/runtimes" />
      </NavigationProvider>
    </QueryClientProvider>);
    return client;
  }
  it.each([
    ["online", "Online", "aurora-dot"],
    ["offline", "Offline", "aurora-dot--warning"],
    ["provisioning", "Preparing", "aurora-dot--running"],
    ["failed", "Failed", "aurora-dot--error"],
    ["unconfigured", "Not set up", "aurora-dot--muted"],
  ])("links a readable %s target with its shared visual vocabulary", async (state, label, dot) => {
    installReads({ ...(TARGET as object), state });
    const client = renderChip();
    const link = await screen.findByRole("link", { name: label });
    expect(link).toHaveAttribute("href", "/acme/runtimes");
    expect(link.firstElementChild).toHaveClass(dot);
    expect(link.firstElementChild).toHaveAttribute("aria-hidden", "true");
    client.clear();
  });
  it("does not claim online when the node is not ready", async () => {
    installReads({ state: "online", node: { ready: false } });
    const client = renderChip();
    expect(await screen.findByRole("link", { name: "Offline" })).toBeVisible();
    client.clear();
  });
  it.each(["pending", "malformed", "failed"])("omits the chip for a %s read", async (kind) => {
    installApi(() => {
      if (kind === "pending") return new Promise(() => {});
      if (kind === "failed") throw new Error("offline");
      return { node: "broken" };
    });
    const client = renderChip();
    if (kind !== "pending") await waitFor(() => expect(client.isFetching()).toBe(0));
    expect(screen.queryByRole("link")).toBeNull();
    client.clear();
  });
});

describe("RuntimeStatus", () => {
  it("wires the execution header and readable node state into the Aurora panel", async () => {
    renderRuntime();
    const state = await screen.findByText("Online");
    const page = screen.getByRole("heading", { name: "Execution", level: 1 }).closest(".aurora-page");
    expect(page).not.toBeNull();
    expect(page?.querySelector("header .aurora-eyebrow")).toBeNull();
    const panel = state.closest("section");
    expect(panel).toHaveClass("aurora-panel");
    expect(page).toContainElement(panel);
    expect(state).toBeVisible();
    expect(panel?.querySelector(".aurora-dot")).toHaveAttribute("aria-hidden", "true");
  });

  it("wires the running list to Aurora rows and the shared readable status pill", async () => {
    installReads(TARGET, [generation()]);
    renderRuntime();
    const row = await screen.findByRole("listitem");
    expect(screen.getByRole("list")).toHaveClass("aurora-list");
    expect(row).toHaveClass("aurora-row");
    expect(within(row).getByText("a launch poster")).toHaveClass("aurora-row-main", "truncate");
    expect(await within(row).findByText("Generating")).toHaveClass("aurora-status-pill", "aurora-status-pill--working");
    expect(screen.getByRole("heading", { name: "Running generations", level: 2 }).parentElement).toHaveClass("aurora-section-head");
  });

  it("uses the panel radius for the pending node skeleton", () => {
    installApi(() => new Promise(() => {}));
    const { container } = renderRuntime();
    expect(container.querySelector('[data-slot="skeleton"]')).toHaveClass("rounded-xl");
    expect(screen.queryByText("Online")).not.toBeInTheDocument();
  });

  it("shows why the runtime is not ready instead of a bare failure", async () => {
    installReads({
      workspaceId: "ws-1",
      state: "failed",
      node: {
        id: "node-1",
        status: "failed",
        ready: false,
        errorCode: "runtime_policy_unavailable",
      },
    });
    renderRuntime();
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "The runtime policy is unavailable. Ask an administrator to check the runtime configuration.",
    );
  });

  it("shows an offline reason and retry without exposing unknown error text", async () => {
    installReads({
      workspaceId: "ws-1",
      state: "offline",
      node: {
        status: "offline",
        ready: false,
        errorCode: "private endpoint credential=secret",
      },
    });
    renderRuntime();
    expect(await screen.findByText("Offline")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(
      "The runtime node is offline.",
    );
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(screen.queryByText(/credential=secret/)).not.toBeInTheDocument();
  });

  it("keeps provisioning guidance for a workspace with no node and offers no retry", async () => {
    installReads({ workspaceId: "ws-1" });
    renderRuntime();
    expect(await screen.findByText("Not set up")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Start a generation to provision one.",
    );
    expect(
      screen.queryByRole("button", { name: "Retry" }),
    ).not.toBeInTheDocument();
  });

  it("offers a retry that re-reads the projection", async () => {
    let reads = 0;
    installApi((path) => {
      if (path === "/api/aurora/generations") return { generations: [] };
      if (path === "/api/aurora/runtime") {
        reads++;
        return reads === 1
          ? {
              workspaceId: "ws-1",
              state: "failed",
              node: { ready: false, status: "failed" },
            }
          : TARGET;
      }
      throw new Error("unexpected path " + path);
    });
    renderRuntime();
    fireEvent.click(await screen.findByRole("button", { name: "Retry" }));
    expect(await screen.findByText("Online")).toBeInTheDocument();
    expect(reads).toBe(2);
  });

  it("shows the online node without a recovery hint", async () => {
    renderRuntime();

    expect(await screen.findByText("Online")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows a provisioning node", async () => {
    installReads({ ...(TARGET as object), state: "provisioning" });
    renderRuntime();

    expect(await screen.findByText("Preparing")).toBeInTheDocument();
  });

  it("gives failed nodes recovery guidance", async () => {
    installReads({ ...(TARGET as object), state: "failed" });
    renderRuntime();

    expect(await screen.findByText("Failed")).toBeInTheDocument();
    expect(
      screen.getByText(/runtime node is offline/i),
    ).toBeInTheDocument();
  });

  it("degrades a malformed target to unconfigured with guidance", async () => {
    // GH #55: a malformed 200 must not crash the screen, and "not set up" must
    // read as guidance rather than as a fact the body never carried.
    installReads({ node: "not-an-object" });
    renderRuntime();

    expect(await screen.findByText("Not set up")).toBeInTheDocument();
    expect(
      screen.getByText(/no running execution node/i),
    ).toBeInTheDocument();
  });

  it("normalizes an unknown server state to unconfigured", async () => {
    installReads({ state: "paused", node: { status: "future", ready: false } });
    renderRuntime();

    expect(await screen.findByText("Not set up")).toBeInTheDocument();
  });

  it("does not crash when the node omits its provider", async () => {
    installReads({
      workspaceId: "ws-1",
      node: {
        id: "node-1",
        status: "starting",
        ready: false,
        createdAt: "2026-10-06T00:00:00Z",
      },
      runtimeId: "rt-1",
      state: "provisioning",
    });
    renderRuntime();

    expect(await screen.findByText("Preparing")).toBeInTheDocument();
  });

  it("shows an in-flight generation with its live status", async () => {
    // The list's stored status is still "queued"; the row labels itself from
    // the polled detail read, which is what makes the screen live.
    installReads(TARGET, [generation({ status: "queued" })]);
    renderRuntime();

    expect(await screen.findByText("a launch poster")).toBeInTheDocument();
    expect(await screen.findByText("Generating")).toBeInTheDocument();
    expect(screen.queryByText("Queued")).not.toBeInTheDocument();
  });

  it.each(["completed", "failed"])(
    "removes a generation from the running list once its detail settles to %s",
    async (settledStatus: string) => {
      // The list read is a one-shot snapshot that still says "queued" — the
      // server has not written the terminal status back — so removal has to be
      // driven by the row's polled detail. Without that the row outlives the
      // generation and only its badge changes.
      let settle: (body: unknown) => void = () => {};
      installApi((path) => {
        if (path === "/api/aurora/runtime") return TARGET;
        if (path === "/api/aurora/generations") {
          return { generations: [generation({ status: "queued" })] };
        }
        if (path === "/api/aurora/generations/gen-1") {
          return new Promise((resolve) => {
            settle = resolve;
          });
        }
        throw new Error(`unexpected path ${path}`);
      });
      renderRuntime();

      // The row is on the running list while its detail read is still in flight.
      expect(await screen.findByText("a launch poster")).toBeInTheDocument();

      await act(async () => {
        settle({
          generation: generation({ status: settledStatus, assets: [] }),
        });
      });

      await waitFor(() =>
        expect(screen.queryByText("a launch poster")).not.toBeInTheDocument(),
      );
      expect(await screen.findByText("Nothing is running.")).toBeInTheDocument();
    },
  );

  it("offers the empty copy when nothing is running", async () => {
    renderRuntime();

    expect(
      await screen.findByText("Nothing is running."),
    ).toBeInTheDocument();
  });
});
