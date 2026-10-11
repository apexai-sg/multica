import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
  AuroraGeneration,
  AuroraSkill,
  AuroraSubscription,
  AuroraTransaction,
} from "@multica/core/aurora";
import { ApiError } from "@multica/core/api";
import { renderWithI18n } from "../test/i18n";
import { stubNavigationAdapter } from "../test/navigation";
import { NavigationProvider } from "../navigation";

const mocks = vi.hoisted(() => ({
  balance: vi.fn(),
  transactions: vi.fn(),
  generations: vi.fn(),
  skills: vi.fn(),
  subscription: vi.fn(),
  topups: vi.fn(),
  createCheckout: vi.fn(),
  createTopupCheckout: vi.fn(),
  openExternal: vi.fn(),
}));

vi.mock("@multica/core/aurora", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/aurora")>();
  return {
    ...actual,
    useAuroraBalance: () => mocks.balance(),
    useAuroraTransactions: () => mocks.transactions(),
    useAuroraGenerations: () => mocks.generations(),
    useAuroraSkills: () => mocks.skills(),
    useAuroraSubscription: () => mocks.subscription(),
    useAuroraTopups: () => mocks.topups(),
    useCreateAuroraCheckout: () => mocks.createCheckout(),
    useCreateAuroraTopupCheckout: () => mocks.createTopupCheckout(),
  };
});

vi.mock("../platform", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../platform")>();
  return { ...actual, openExternal: mocks.openExternal };
});

vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return { ...actual, useWorkspaceSlug: () => "acme" };
});

import { AuroraBilling } from "./billing";

const MICRO = 1_000_000;

const POSTER: AuroraSkill = {
  id: "poster",
  name: "海报制作",
  nameEn: "Poster",
  category: "image",
  credits: 76,
  input: ["text"],
  output: ["image"],
  featured: false,
  available: true,
  attachments: [],
};

const GENERATION: AuroraGeneration = {
  id: "gen-1",
  skillId: "poster",
  prompt: "a launch poster",
  status: "completed",
  creditsReserved: 76 * MICRO,
  creditsCharged: 76 * MICRO,
  error: null,
  createdAt: "2026-09-23T00:00:00Z",
  taskId: "",
};

function subscription(
  overrides: Partial<AuroraSubscription> = {},
): AuroraSubscription {
  return {
    tier: "free",
    status: "",
    currentPeriodEnd: null,
    cancelAtPeriodEnd: false,
    limits: { generationsPerMonth: 10, concurrency: 1 },
    usage: { generationsUsedThisMonth: 0, activeGenerations: 0 },
    ...overrides,
  };
}

function transaction(
  overrides: Partial<AuroraTransaction> = {},
): AuroraTransaction {
  return {
    id: "txn-1",
    kind: "deduction",
    amountMicro: -76 * MICRO,
    balanceAfterMicro: 924 * MICRO,
    reference: "gen-1",
    createdAt: "2026-09-23T00:00:00Z",
    ...overrides,
  };
}

function mutation() {
  return { mutate: vi.fn(), isPending: false, error: null };
}

/**
 * The origin `useAppOrigin` derives from the adapter. A stub that echoes the
 * path (the default) is not an absolute URL, so the plan card would have no
 * return URLs and its buttons would be disabled — which is its own test, not
 * the default state.
 */
const APP_ORIGIN = "https://app.example.com";

function renderBilling(searchParams = new URLSearchParams()) {
  return renderWithI18n(
    <NavigationProvider
      value={stubNavigationAdapter({
        searchParams,
        getShareableUrl: (path) => new URL(path, APP_ORIGIN).toString(),
      })}
    >
      <AuroraBilling />
    </NavigationProvider>,
  );
}

/**
 * The Subscribe button on one plan's row. Both paid plans share the button's
 * label, so the row is what tells them apart.
 */
function subscribeButtonFor(plan: string): HTMLElement {
  const button = screen
    .getAllByRole("button", { name: "Subscribe" })
    .find((candidate) => candidate.closest("li")?.textContent?.includes(plan));
  if (!button) throw new Error(`no subscribe button on the ${plan} row`);
  return button;
}

/**
 * A resolved read, shaped the way `parseAurora*` hands it to the query: the
 * payload plus whether the body behind it was readable.
 */
function read<T>(value: T, degraded = false) {
  return { data: { value, degraded }, isPending: false };
}

describe("AuroraBilling", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.balance.mockReturnValue({
      ...read({ availableMicro: 1000 * MICRO }),
      // The error state's retry re-runs both reads, so the wallet needs one too
      // even in the cases that never reach it.
      refetch: vi.fn(),
    });
    // The error state's retry re-runs the ledger read too, so the ledger needs
    // a refetch even in the cases that never reach it.
    mocks.transactions.mockReturnValue({
      ...read([transaction()]),
      refetch: vi.fn(),
    });
    mocks.generations.mockReturnValue(read([GENERATION]));
    mocks.skills.mockReturnValue(read([POSTER]));
    mocks.subscription.mockReturnValue({
      data: subscription(),
      isPending: false,
      refetch: vi.fn(),
    });
    mocks.topups.mockReturnValue({
      data: [
        { id: "t5", credits: 5000 },
        { id: "t20", credits: 20000 },
      ],
      isPending: false,
      refetch: vi.fn(),
    });
    mocks.createCheckout.mockReturnValue(mutation());
    mocks.createTopupCheckout.mockReturnValue(mutation());
  });

  it("assigns real wallet, usage and loaded-row facts to separate stat cards", () => {
    mocks.subscription.mockReturnValue({
      data: subscription({
        tier: "creator",
        status: "active",
        limits: { generationsPerMonth: 30, concurrency: 2 },
        usage: { generationsUsedThisMonth: 12, activeGenerations: 1 },
      }),
      isPending: false,
    });
    mocks.transactions.mockReturnValue(read([
      transaction(),
      transaction({ id: "txn-2", kind: "topup", amountMicro: 500 * MICRO }),
    ]));

    renderBilling();

    const cards = screen.getAllByRole("article");
    expect(cards).toHaveLength(3);
    expect(within(cards[0]!).getByText("Available credits")).toBeVisible();
    expect(within(cards[0]!).getByText("1,000")).toBeVisible();
    expect(within(cards[1]!).getByText("12 of 30")).toBeVisible();
    expect(within(cards[2]!).getByText("2")).toBeVisible();
    const meter = screen.getByRole("progressbar");
    expect(meter).toHaveAttribute("aria-valuenow", "40");
    const usageLabel = meter.getAttribute("aria-label")!;
    expect(within(cards[1]!).getByText(usageLabel)).toBeVisible();
    expect(screen.getAllByText(usageLabel)).toHaveLength(1);
    expect(screen.getAllByText("12 of 30")).toHaveLength(1);
    expect(meter.closest("section")).not.toContainElement(cards[1]!);
  });

  it("omits an unavailable usage stat and counts an empty ledger as zero", () => {
    mocks.subscription.mockReturnValue({ data: undefined, isPending: false });
    mocks.transactions.mockReturnValue(read([]));

    renderBilling();

    const cards = screen.getAllByRole("article");
    expect(cards).toHaveLength(2);
    expect(within(cards[0]!).getByText("1,000")).toBeVisible();
    expect(within(cards[1]!).getByText("0")).toBeVisible();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
    expect(screen.getByText("No activity yet")).toBeVisible();
  });

  it("keeps every native pack busy and inert while checkout is pending", async () => {
    const user = userEvent.setup();
    const checkout = { ...mutation(), isPending: true };
    mocks.createTopupCheckout.mockReturnValue(checkout);
    renderBilling();

    for (const name of ["5,000 credits · $5", "20,000 credits · $20"]) {
      const pack = screen.getByRole("button", { name });
      expect(pack).toHaveAttribute("type", "button");
      expect(pack).toHaveAttribute("aria-busy", "true");
      expect(pack).toBeDisabled();
      await user.click(pack);
    }
    expect(checkout.mutate).not.toHaveBeenCalled();
  });

  it("splits pack facts visually without changing the second pack checkout or navigation", async () => {
    const user = userEvent.setup();
    const checkout = {
      ...mutation(),
      mutate: vi.fn((_request: unknown, options?: { onSuccess?: (url: string) => void }) =>
        options?.onSuccess?.("https://checkout.aurora-fixture.test/topup")),
    };
    mocks.createTopupCheckout.mockReturnValue(checkout);
    renderBilling();

    const pack = screen.getByRole("button", { name: "20,000 credits · $20" });
    expect(within(pack).getByText("20,000 credits")).toBeVisible();
    expect(within(pack).getByText("$20")).toBeVisible();
    expect(pack).toHaveAttribute("aria-busy", "false");
    await user.click(pack);
    expect(checkout.mutate.mock.calls[0]?.[0]).toEqual({
      topupId: "t20",
      successUrl: "https://app.example.com/acme/billing?checkout=success",
      cancelUrl: "https://app.example.com/acme/billing?checkout=cancel",
    });
    expect(mocks.openExternal).toHaveBeenCalledWith(
      "https://checkout.aurora-fixture.test/topup", { webTarget: "same-tab" },
    );
  });

  it("keeps native packs disabled without a return origin", async () => {
    const user = userEvent.setup();
    const checkout = mutation();
    mocks.createTopupCheckout.mockReturnValue(checkout);
    renderWithI18n(
      <NavigationProvider value={stubNavigationAdapter()}>
        <AuroraBilling />
      </NavigationProvider>,
    );
    const pack = screen.getByRole("button", { name: "5,000 credits · $5" });
    expect(pack).toBeDisabled();
    expect(pack).toHaveAttribute("aria-busy", "false");
    await user.click(pack);
    expect(checkout.mutate).not.toHaveBeenCalled();
  });

  it("shows the wallet in credits rather than micro-credits", () => {
    renderBilling();

    expect(screen.getByText("1,000")).toBeInTheDocument();
    expect(screen.getByText("Available credits")).toBeInTheDocument();
  });

  it("attributes a generation's charge to the skill that made it", () => {
    renderBilling();

    // The ledger carries a generation id, which means nothing to a reader; the
    // catalog is what turns it back into a skill name.
    expect(screen.getByText("Poster")).toBeInTheDocument();
    expect(screen.getByText("−76")).toBeInTheDocument();
    expect(screen.getByText("Balance after: 924")).toBeInTheDocument();
  });

  it("falls back to the kind's label when the reference is not a generation", () => {
    // Grants and subscription purchases key their rows with their own
    // references (`signup:`, `sub:`), which no catalog can resolve.
    mocks.transactions.mockReturnValue(
      read([
        transaction({
          id: "txn-2",
          kind: "topup",
          amountMicro: 500 * MICRO,
          reference: "signup:2026-09",
        }),
      ]),
    );

    renderBilling();

    expect(screen.getByText("Top-up")).toBeInTheDocument();
    expect(screen.getByText("+500")).toBeInTheDocument();
  });

  it("labels a ledger kind this build has never seen", () => {
    mocks.transactions.mockReturnValue(
      read([transaction({ kind: "chargeback", reference: "unknown:1" })]),
    );

    renderBilling();

    expect(screen.getByText("Other")).toBeInTheDocument();
  });

  it("offers the empty copy before the wallet has moved", () => {
    mocks.transactions.mockReturnValue(read([]));

    renderBilling();

    expect(screen.getByText("No activity yet")).toBeInTheDocument();
  });

  it("reports a corrupted balance as a failed load rather than 0 credits", () => {
    // GH #55, the sharpest case: a malformed body parses to `availableMicro: 0`,
    // which resolves the query and used to render as "0 credits" — a statement
    // about the user's wallet that invites a top-up or a support ticket.
    mocks.balance.mockReturnValue(
      read({ availableMicro: 0 }, true),
    );

    renderBilling();

    expect(screen.getByText("Could not load your credits")).toBeInTheDocument();
    expect(screen.queryByText("0")).not.toBeInTheDocument();
    expect(screen.queryByText("Available credits")).not.toBeInTheDocument();
  });

  it("reports a corrupted ledger as a failed load rather than an empty one", () => {
    mocks.transactions.mockReturnValue(read([], true));

    renderBilling();

    expect(screen.getByText("Could not load your credits")).toBeInTheDocument();
    expect(screen.queryByText("No activity yet")).not.toBeInTheDocument();
  });

  it("reports a failed load rather than an empty ledger", async () => {
    // "No activity yet" would be a lie with a way forward missing: a ledger
    // that would not load is not a ledger that has never moved.
    const user = userEvent.setup();
    const refetch = vi.fn();
    mocks.transactions.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
      refetch,
    });

    renderBilling();

    expect(screen.getByText("Could not load your credits")).toBeInTheDocument();
    expect(screen.queryByText("No activity yet")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(refetch).toHaveBeenCalled();
  });

  it("keeps the ledger it already read when a refetch fails", () => {
    // The same rule the library follows: a failed read is only fatal when it
    // left nothing behind. Replacing a readable balance and ledger with an
    // error card because one background refetch dropped is the worse trade.
    mocks.transactions.mockReturnValue({
      ...read([transaction()]),
      isError: true,
      refetch: vi.fn(),
    });

    renderBilling();

    expect(screen.getByText("Usage")).toBeInTheDocument();
    expect(
      screen.queryByText("Could not load your credits"),
    ).not.toBeInTheDocument();
  });

  it("reports a failed subscription read instead of hiding the plan card", async () => {
    const user = userEvent.setup();
    const refetch = vi.fn();
    mocks.subscription.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
      refetch,
    });

    renderBilling();

    expect(screen.getByText("Could not load your credits")).toBeInTheDocument();
    expect(screen.queryByText("Plan")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(refetch).toHaveBeenCalled();
  });

  it("reports a failed top-up catalog read instead of claiming it is empty", async () => {
    const user = userEvent.setup();
    const refetch = vi.fn();
    mocks.topups.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
      refetch,
    });

    renderBilling();

    expect(screen.getByText("Could not load your credits")).toBeInTheDocument();
    expect(screen.queryByText("No top-up packs available.")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(refetch).toHaveBeenCalled();
  });

  it("polls billing state after returning from a successful checkout", () => {
    vi.useFakeTimers();
    const balanceRefetch = vi.fn();
    const transactionsRefetch = vi.fn();
    const subscriptionRefetch = vi.fn();
    mocks.balance.mockReturnValue({
      ...read({ availableMicro: 1000 * MICRO }),
      refetch: balanceRefetch,
    });
    mocks.transactions.mockReturnValue({
      ...read([transaction()]),
      refetch: transactionsRefetch,
    });
    mocks.subscription.mockReturnValue({
      data: subscription(),
      isPending: false,
      refetch: subscriptionRefetch,
    });

    renderBilling(new URLSearchParams("checkout=success"));

    expect(balanceRefetch).toHaveBeenCalledTimes(1);
    expect(transactionsRefetch).toHaveBeenCalledTimes(1);
    expect(subscriptionRefetch).toHaveBeenCalledTimes(1);

    act(() => vi.advanceTimersByTime(2_000));

    expect(balanceRefetch).toHaveBeenCalledTimes(2);
    expect(transactionsRefetch).toHaveBeenCalledTimes(2);
    expect(subscriptionRefetch).toHaveBeenCalledTimes(2);
    vi.useRealTimers();
  });

  it("names the plan and measures usage against its limit", () => {
    mocks.subscription.mockReturnValue({
      data: subscription({
        tier: "creator",
        status: "active",
        limits: { generationsPerMonth: 30, concurrency: 2 },
        usage: { generationsUsedThisMonth: 12, activeGenerations: 1 },
      }),
      isPending: false,
    });

    renderBilling();

    expect(screen.getByText("Plan")).toBeInTheDocument();
    expect(screen.getByText("Creator")).toBeInTheDocument();
    expect(screen.getByText("Active")).toBeInTheDocument();
    expect(screen.getByText("12 of 30")).toBeInTheDocument();
  });

  it("shows the renewal date of a plan that has one", () => {
    mocks.subscription.mockReturnValue({
      data: subscription({
        tier: "pro",
        status: "active",
        currentPeriodEnd: "2026-10-24T00:00:00Z",
      }),
      isPending: false,
    });

    renderBilling();

    expect(screen.getByText("Renews Oct 24, 2026")).toBeInTheDocument();
  });

  it("does not promise renewal when cancellation is scheduled", () => {
    mocks.subscription.mockReturnValue({
      data: subscription({
        tier: "pro",
        status: "active",
        currentPeriodEnd: "2026-10-24T00:00:00Z",
        cancelAtPeriodEnd: true,
      }),
      isPending: false,
    });

    renderBilling();

    expect(screen.queryByText("Renews Oct 24, 2026")).not.toBeInTheDocument();
    expect(screen.getByText("Ends at the end of the period.")).toBeInTheDocument();
  });

  it("does not promise renewal for a canceled subscription", () => {
    mocks.subscription.mockReturnValue({
      data: subscription({
        status: "canceled",
        currentPeriodEnd: "2026-10-24T00:00:00Z",
      }),
      isPending: false,
    });

    renderBilling();

    expect(screen.queryByText("Renews Oct 24, 2026")).not.toBeInTheDocument();
  });

  it("offers no subscribe button while a live plan exists", () => {
    mocks.subscription.mockReturnValue({
      data: subscription({ tier: "creator", status: "active" }),
      isPending: false,
    });

    renderBilling();

    expect(
      screen.queryByRole("button", { name: "Subscribe" }),
    ).not.toBeInTheDocument();
  });

  it("fails closed when the server reports an unknown subscription status", () => {
    mocks.subscription.mockReturnValue({
      data: subscription({ tier: "creator", status: "paused" }),
      isPending: false,
    });

    renderBilling();

    expect(
      screen.queryByRole("button", { name: "Subscribe" }),
    ).not.toBeInTheDocument();
  });

  it("offers the paid plans to a user who has none", () => {
    renderBilling();

    // The header names the plan the user is on, and the catalogue below lists
    // it again beside its price: that repetition is the menu.
    expect(screen.getAllByText("Free")).toHaveLength(2);
    expect(screen.getByText("$0")).toBeInTheDocument();
    expect(screen.getByText("200 credits a month")).toBeInTheDocument();
    expect(screen.getByText("$9.90/mo")).toBeInTheDocument();
    expect(screen.getByText("$29/mo")).toBeInTheDocument();
    // The free plan is the one they are on; only the paid plans are for sale.
    expect(screen.getAllByRole("button", { name: "Subscribe" })).toHaveLength(2);
  });

  it("starts a subscription checkout with the workspace's return URLs", async () => {
    const user = userEvent.setup();
    const createCheckout = mutation();
    mocks.createCheckout.mockReturnValue(createCheckout);

    renderBilling();
    await user.click(subscribeButtonFor("Creator"));

    // The API host is not a page the user can be sent back to, so the client
    // builds the return URLs from its own origin and the workspace slug route.
    expect(createCheckout.mutate.mock.calls[0]?.[0]).toEqual({
      tier: "creator",
      billingCycle: "monthly",
      successUrl: "https://app.example.com/acme/billing?checkout=success",
      cancelUrl: "https://app.example.com/acme/billing?checkout=cancel",
    });
  });

  it("starts a top-up checkout for the pack that was clicked", async () => {
    const user = userEvent.setup();
    const createTopupCheckout = mutation();
    mocks.createTopupCheckout.mockReturnValue(createTopupCheckout);

    renderBilling();
    await user.click(
      screen.getByRole("button", { name: "5,000 credits · $5" }),
    );

    expect(createTopupCheckout.mutate.mock.calls[0]?.[0]).toEqual({
      topupId: "t5",
      successUrl: "https://app.example.com/acme/billing?checkout=success",
      cancelUrl: "https://app.example.com/acme/billing?checkout=cancel",
    });
  });

  it("hands a successful checkout URL to the platform external navigator", async () => {
    const user = userEvent.setup();
    const createCheckout = {
      mutate: vi.fn(
        (
          _request: unknown,
          options?: { onSuccess?: (checkoutUrl: string) => void },
        ) => options?.onSuccess?.("https://checkout.stripe.com/c/1"),
      ),
      isPending: false,
      error: null,
    };
    mocks.createCheckout.mockReturnValue(createCheckout);

    renderBilling();
    await user.click(subscribeButtonFor("Creator"));

    expect(mocks.openExternal).toHaveBeenCalledWith(
      "https://checkout.stripe.com/c/1",
      { webTarget: "same-tab" },
    );
  });

  it("says the instance does not sell plans when payments are unavailable", () => {
    mocks.createCheckout.mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      error: new ApiError("payments not configured", 503, "Unavailable"),
    });

    renderBilling();

    expect(
      screen.getByText("Payments are not available on this instance."),
    ).toBeInTheDocument();
  });

  it("says a subscription already exists when the checkout is refused", () => {
    // A 409 and a 503 need different words: one is a state the user is already
    // in, the other is a state the deployment is in.
    mocks.createTopupCheckout.mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      error: new ApiError("subscription already exists", 409, "Conflict"),
    });

    renderBilling();

    expect(
      screen.getByText("You already have a subscription."),
    ).toBeInTheDocument();
  });
});
