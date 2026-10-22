import type { Workspace } from "@multica/core/types";

/**
 * Aurora's own route space, bound to a workspace slug.
 *
 * These live in the app rather than in `@multica/core/paths` because they are
 * this app's surface: the shared path builders describe the Multica workspace
 * routes (issues, agents, projects) that web and desktop serve, and Aurora
 * serves none of them. The shared views never build an Aurora URL themselves —
 * they take the library and checkout routes as props, which is what keeps
 * `packages/views` free of app routing.
 */
const encode = (value: string) => encodeURIComponent(value);

// The server's workspace slug rule. Used to vet a slug read from a cookie
// before it is spliced into a redirect path: the value is attacker-writable,
// and while a URL built from it stays on this origin, a slug that could not
// name a workspace has no business becoming a route. A rejected value falls
// through to /login, which resolves the destination from the workspace list
// instead — so a false negative costs a round-trip, never a dead end.
const WORKSPACE_SLUG_PATTERN = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

export function isWorkspaceSlug(value: string | null | undefined): value is string {
  return typeof value === "string" && WORKSPACE_SLUG_PATTERN.test(value);
}

export function auroraRoutes(slug: string) {
  const ws = `/${encode(slug)}`;
  return {
    skills: () => `${ws}/skills`,
    catalog: () => `${ws}/skills/connected`,
    tools: () => `${ws}/tools`,
    tool: (toolId: string) => `${ws}/tools/${encode(toolId)}`,
    works: () => `${ws}/works`,
    // One generation, under the works destination it belongs to.
    work: (generationId: string) => `${ws}/works/${encode(generationId)}`,
    history: () => `${ws}/history`,
    runtime: () => `${ws}/runtimes`,
    billing: () => `${ws}/billing`,
  };
}

/**
 * Where a signed-in user with this workspace list belongs, or null when there
 * is no workspace to open.
 *
 * Registration opens a personal workspace server-side on every login, so an
 * authenticated user normally has exactly one. The null branch is not a
 * destination because this app serves no route for it: a caller that gets null
 * has to say so on the page it is already on rather than navigating somewhere
 * that does not exist.
 *
 * Aurora deliberately does not consult `onboarded_at`, which gates `/slug/*` in
 * apps/web behind the Multica questionnaire. That flow produces a Multica
 * workspace for coding agents; a consumer opening a poster maker has nothing to
 * answer there, and the workspace it needs already exists.
 */
export function resolveAuroraDestination(
  workspaces: Workspace[],
): string | null {
  const first = workspaces[0];
  return first ? auroraRoutes(first.slug).skills() : null;
}
