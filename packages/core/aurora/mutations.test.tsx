/**
 * @vitest-environment jsdom
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import { isAuroraInsufficientCreditsError } from "./api";
import {
  useCancelAuroraSubscription,
  useCreateAuroraCheckout,
  useCreateAuroraGeneration,
  useCreateAuroraTopupCheckout,
  useDeleteAuroraAsset,
} from "./mutations";
import { auroraKeys, auroraWalletKeys } from "./queries";
import type { AuroraAsset } from "./schema";

vi.mock("../hooks", () => ({ useWorkspaceId: () => "ws-1" }));

const asset: AuroraAsset = {
  id: "asset-1",
  generationId: "gen-1",
  kind: "image",
  mediaUrl: "https://cdn.example/a.png",
  format: "png",
  createdAt: "2026-09-22T00:00:00Z",
};

function wrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

/**
 * jsdom cannot navigate, and the checkout mutations exist to do exactly that:
 * replacing `location` keeps the destination observable and stops jsdom from
 * logging "Not implemented: navigation" for every checkout test.
 */
function stubNavigation() {
  const assign = vi.fn();
  vi.stubGlobal("location", { assign });
  return assign;
}

function newClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
}

/** The query keys an invalidation sweep touched, as comparable strings. */
function invalidatedKeys(invalidate: { mock: { calls: unknown[][] } }): string[] {
  return invalidate.mock.calls.map(([arg]) =>
    JSON.stringify((arg as { queryKey?: unknown })?.queryKey),
  );
}

describe("useCreateAuroraGeneration", () => {
  it("refreshes the wallet and the generations once the create settles", async () => {
    setApiInstance({
      requestJson: vi.fn(async () => ({
        generation: {
          id: "gen-1",
          skillId: "poster",
          prompt: "a cat",
          status: "queued",
          creditsReserved: 76,
        },
      })),
    } as unknown as ApiClient);
    const qc = newClient();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    const { result } = renderHook(() => useCreateAuroraGeneration(), {
      wrapper: wrapper(qc),
    });

    await act(async () => {
      await result.current.mutateAsync({ skillId: "poster", prompt: "a cat" });
    });

    const keys = invalidatedKeys(invalidate);
    // Create reserved credits before it returned, so the balance on screen is
    // stale the moment it settles.
    expect(keys).toContain(JSON.stringify(auroraWalletKeys.all()));
    expect(keys).toContain(JSON.stringify(auroraKeys.generations("ws-1")));
  });

  it("hands a 402 to the caller and still refreshes the wallet", async () => {
    setApiInstance({
      requestJson: vi.fn(async () => {
        throw new ApiError("insufficient credits", 402, "Payment Required");
      }),
    } as unknown as ApiClient);
    const qc = newClient();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    const { result } = renderHook(() => useCreateAuroraGeneration(), {
      wrapper: wrapper(qc),
    });

    await act(async () => {
      await expect(
        result.current.mutateAsync({ skillId: "poster", prompt: "a cat" }),
      ).rejects.toMatchObject({ status: 402 });
    });

    await waitFor(() =>
      expect(isAuroraInsufficientCreditsError(result.current.error)).toBe(true),
    );
    // The wallet the user is looking at was read before another surface spent
    // it, which is exactly how a 402 happens — so it is refreshed on failure
    // too.
    expect(invalidatedKeys(invalidate)).toContain(
      JSON.stringify(auroraWalletKeys.all()),
    );
  });
});

describe("useDeleteAuroraAsset", () => {
  it("leaves the cached library alone until the server confirms", async () => {
    let release: (() => void) | undefined;
    const write = new Promise<void>((resolve) => {
      release = resolve;
    });
    setApiInstance({
      requestJson: vi.fn(() => write),
    } as unknown as ApiClient);
    const qc = newClient();
    qc.setQueryData(auroraKeys.assets("ws-1"), [asset]);
    const { result } = renderHook(() => useDeleteAuroraAsset(), {
      wrapper: wrapper(qc),
    });

    act(() => {
      void result.current.mutate(asset.id);
    });

    // Not optimistic: the server also drops the stored object, so a row that
    // survived a rolled-back delete would offer a file that is already gone.
    expect(qc.getQueryData(auroraKeys.assets("ws-1"))).toEqual([asset]);

    await act(async () => {
      release?.();
      await write;
    });
  });

  it("refreshes the library and the generation list once the delete settles", async () => {
    setApiInstance({
      requestJson: vi.fn(async () => undefined),
    } as unknown as ApiClient);
    const qc = newClient();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    const { result } = renderHook(() => useDeleteAuroraAsset(), {
      wrapper: wrapper(qc),
    });

    await act(async () => {
      await result.current.mutateAsync(asset.id);
    });

    const keys = invalidatedKeys(invalidate);
    expect(keys).toContain(JSON.stringify(auroraKeys.assets("ws-1")));
    // The same asset is listed inside its generation's detail.
    expect(keys).toContain(JSON.stringify(auroraKeys.generations("ws-1")));
  });
});

describe("useCreateAuroraCheckout", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("returns the checkout URL without performing browser navigation", async () => {
    setApiInstance({
      requestJson: vi.fn(async () => ({
        checkoutUrl: "https://checkout.stripe.com/c/1",
      })),
    } as unknown as ApiClient);
    const assign = stubNavigation();
    const { result } = renderHook(() => useCreateAuroraCheckout(), {
      wrapper: wrapper(newClient()),
    });

    let checkoutUrl: string | undefined;
    await act(async () => {
      checkoutUrl = await result.current.mutateAsync({
        tier: "creator",
        billingCycle: "monthly",
        provider: "stripe",
        successUrl: "https://app.example.com/acme/billing?checkout=success",
        cancelUrl: "https://app.example.com/acme/billing?checkout=cancel",
      });
    });

    expect(checkoutUrl).toBe("https://checkout.stripe.com/c/1");
    expect(assign).not.toHaveBeenCalled();
  });

  it("posts the chosen provider in the checkout body", async () => {
    // The channel is the user's choice: the server must be told which one, not
    // left to its own Stripe default.
    const requestJson = vi.fn(
      async (_path: string, _init?: { method?: string; body?: string }) => ({
        checkoutUrl: "https://checkout.antom.test/c/1",
      }),
    );
    setApiInstance({ requestJson } as unknown as ApiClient);
    stubNavigation();
    const { result } = renderHook(() => useCreateAuroraCheckout(), {
      wrapper: wrapper(newClient()),
    });

    await act(async () => {
      await result.current.mutateAsync({
        tier: "pro",
        billingCycle: "monthly",
        provider: "antom",
        successUrl: "https://app.example.com/acme/billing?checkout=success",
        cancelUrl: "https://app.example.com/acme/billing?checkout=cancel",
      });
    });

    const [path, init] = requestJson.mock.calls[0] ?? [];
    expect(path).toBe("/api/aurora/billing/checkout");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(init?.body ?? "{}")).toEqual({
      tier: "pro",
      billingCycle: "monthly",
      provider: "antom",
      successUrl: "https://app.example.com/acme/billing?checkout=success",
      cancelUrl: "https://app.example.com/acme/billing?checkout=cancel",
    });
  });

  it("posts the plan and refreshes the plan and the wallet", async () => {
    // The purchase completes on Stripe's origin, so this client never sees the
    // webhook that grants the credits: the invalidation is what makes the
    // return trip re-read both.
    const requestJson = vi.fn(async () => ({
      checkoutUrl: "https://checkout.stripe.com/c/1",
    }));
    setApiInstance({ requestJson } as unknown as ApiClient);
    stubNavigation();
    const qc = newClient();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    const { result } = renderHook(() => useCreateAuroraCheckout(), {
      wrapper: wrapper(qc),
    });

    await act(async () => {
      await result.current.mutateAsync({
        tier: "creator",
        billingCycle: "monthly",
        provider: "stripe",
        successUrl: "https://app.example.com/acme/billing?checkout=success",
        cancelUrl: "https://app.example.com/acme/billing?checkout=cancel",
      });
    });

    expect(requestJson).toHaveBeenCalledWith(
      "/api/aurora/billing/checkout",
      expect.objectContaining({ method: "POST" }),
    );
    const keys = invalidatedKeys(invalidate);
    expect(keys).toContain(JSON.stringify(auroraWalletKeys.subscription()));
    expect(keys).toContain(JSON.stringify(auroraWalletKeys.balance()));
  });
});

describe("useCancelAuroraSubscription", () => {
  it("posts to the cancel endpoint and refreshes the plan", async () => {
    const requestJson = vi.fn(async () => ({
      subscription: {
        tier: "creator",
        status: "active",
        provider: "antom",
        cancelAtPeriodEnd: true,
      },
    }));
    setApiInstance({ requestJson } as unknown as ApiClient);
    const qc = newClient();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    const setQueryData = vi.spyOn(qc, "setQueryData");
    const { result } = renderHook(() => useCancelAuroraSubscription(), {
      wrapper: wrapper(qc),
    });

    let canceled: { cancelAtPeriodEnd: boolean } | undefined;
    await act(async () => {
      canceled = await result.current.mutateAsync();
    });

    expect(requestJson).toHaveBeenCalledWith(
      "/api/aurora/billing/subscription/cancel",
      expect.objectContaining({ method: "POST" }),
    );
    // The endpoint answers with the plan itself, so the caller sees the
    // scheduled cancellation without a second read.
    expect(canceled?.cancelAtPeriodEnd).toBe(true);
    // That same body is the plan cache's new content, which is what makes the
    // card show the scheduled cancellation immediately.
    expect(setQueryData).toHaveBeenCalledWith(
      auroraWalletKeys.subscription(),
      expect.objectContaining({ cancelAtPeriodEnd: true }),
    );
    const keys = invalidatedKeys(invalidate);
    expect(keys).toContain(JSON.stringify(auroraWalletKeys.subscription()));
    // Cancelling at period end does not move the wallet.
    expect(keys).not.toContain(JSON.stringify(auroraWalletKeys.balance()));
  });

  it("hands a provider failure to the caller without touching the cache", async () => {
    setApiInstance({
      requestJson: vi.fn(async () => {
        throw new ApiError("failed to cancel subscription", 502, "Bad Gateway");
      }),
    } as unknown as ApiClient);
    const qc = newClient();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    const setQueryData = vi.spyOn(qc, "setQueryData");
    const { result } = renderHook(() => useCancelAuroraSubscription(), {
      wrapper: wrapper(qc),
    });

    await act(async () => {
      await expect(result.current.mutateAsync()).rejects.toMatchObject({
        status: 502,
      });
    });

    // A 502 means Antom refused: the server changed nothing, so the cached
    // plan is left alone and the screen reports the failure.
    expect(invalidatedKeys(invalidate)).not.toContain(
      JSON.stringify(auroraWalletKeys.subscription()),
    );
    expect(setQueryData).not.toHaveBeenCalled();
  });
});

describe("useCreateAuroraTopupCheckout", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("posts the pack and refreshes only the wallet", async () => {
    const requestJson = vi.fn(async () => ({
      checkoutUrl: "https://checkout.stripe.com/c/2",
    }));
    setApiInstance({ requestJson } as unknown as ApiClient);
    stubNavigation();
    const qc = newClient();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    const { result } = renderHook(() => useCreateAuroraTopupCheckout(), {
      wrapper: wrapper(qc),
    });

    await act(async () => {
      await result.current.mutateAsync({
        topupId: "t5",
        successUrl: "https://app.example.com/acme/billing?checkout=success",
        cancelUrl: "https://app.example.com/acme/billing?checkout=cancel",
      });
    });

    expect(requestJson).toHaveBeenCalledWith(
      "/api/aurora/billing/topup/checkout",
      expect.objectContaining({ method: "POST" }),
    );
    const keys = invalidatedKeys(invalidate);
    expect(keys).toContain(JSON.stringify(auroraWalletKeys.balance()));
    // A top-up buys credits, not a plan.
    expect(keys).not.toContain(JSON.stringify(auroraWalletKeys.subscription()));
  });
});
