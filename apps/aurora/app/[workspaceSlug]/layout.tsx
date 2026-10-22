"use client";

import { use, useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { useRouter, usePathname } from "next/navigation";
import { WorkspaceSlugProvider, paths } from "@multica/core/paths";
import { workspaceBySlugOptions } from "@multica/core/workspace";
import { setCurrentWorkspace } from "@multica/core/platform";
import { useAuthStore } from "@multica/core/auth";
import { useT } from "@multica/views/i18n";
import { useWorkspaceSeen } from "@multica/views/workspace/use-workspace-seen";
import { Sparkles } from "lucide-react";
import { workspaceSlugFromPathname } from "@multica/core/workspace/workspace-slug-from-pathname";
import { WorkspaceChrome } from "@/components/workspace-chrome";
import { WorkspaceUnavailable } from "@/components/workspace-unavailable";

/**
 * The workspace boundary: it turns a URL slug into the platform's current
 * workspace, and refuses to render anything below until that resolved.
 *
 * Aurora drops the second gate apps/web applies here. That one bounces a user
 * whose `onboarded_at` is null to the Multica questionnaire, which exists to
 * set up a workspace for coding agents; a consumer opening a poster maker has
 * nothing to answer there, and no Aurora endpoint requires it. Every other
 * rule — auth, slug resolution, the header the first child query will send —
 * is the same one web enforces, and is enforced the same way.
 */
export default function WorkspaceLayout({
  children,
  params,
}: {
  children: React.ReactNode;
  params: Promise<{ workspaceSlug: string }>;
}) {
  const { workspaceSlug } = use(params);
  const { t: tCommon } = useT("common");
  const user = useAuthStore((s) => s.user);
  const isAuthLoading = useAuthStore((s) => s.isLoading);
  const router = useRouter();
  const pathname = usePathname();

  // Workspace routes require auth. Without this the layout renders null and the
  // visitor sees a blank page stuck on /{slug}/... — the common case being a
  // bookmarked or shared link opened in a signed-out browser. `next` carries
  // the destination across the sign-in so the link still lands where it pointed.
  useEffect(() => {
    if (!isAuthLoading && !user) {
      router.replace(`${paths.login()}?next=${encodeURIComponent(pathname)}`);
    }
  }, [isAuthLoading, user, router, pathname]);

  // Resolve the slug through the shared workspace-list query. A warm auth
  // bootstrap reuses its cache; a cold route fetches it. Disabled until
  // identity has been verified — there is no header to send before that.
  const { data: workspace } = useQuery({
    ...workspaceBySlugOptions(workspaceSlug),
    enabled: !!user,
  });

  // Render-phase sync: feed the URL slug into the platform singleton so the
  // first child query's X-Workspace-Slug header is already correct.
  // setCurrentWorkspace self-dedupes and runs rehydrate as a side effect, so
  // calling it on every render is safe.
  //
  // Gated on the live pathname because this is a module-global write from
  // render, and the App Router can keep a previous workspace's layout mounted
  // beside the incoming one. Both instances re-render on their own query
  // activity, so an unguarded write let them alternate — every render flipping
  // the singleton back to its own slug, tearing down and rebuilding the
  // realtime socket bound to it. Comparing against the pathname resolves that
  // without an ownership handshake: the URL is the source of truth for
  // workspace identity, and usePathname() is one value shared by every mounted
  // instance, so exactly one layout — the routed one — can define it.
  if (workspace && workspaceSlug === workspaceSlugFromPathname(pathname)) {
    setCurrentWorkspace(workspaceSlug, workspace.id);
  }

  // Cookie write (last_workspace_slug) — the root page reads it on the next
  // visit to decide between this workspace and /login.
  useEffect(() => {
    if (!workspace || typeof document === "undefined") return;
    const oneYear = 60 * 60 * 24 * 365;
    const secure = location.protocol === "https:" ? "; Secure" : "";
    document.cookie = `last_workspace_slug=${encodeURIComponent(workspaceSlug)}; path=/; max-age=${oneYear}; SameSite=Lax${secure}`;
  }, [workspace, workspaceSlug]);

  // Remember whether this slug has resolved before. A workspace that disappears
  // from under us — deleted elsewhere, or the user removed from it — is a
  // relocate, not a dead end: shared core answers it with a full-page navigation
  // to the destination this app registered (packages/core/realtime/use-realtime-sync.ts
  // asking platform/workspace-destination.ts), and that fetch is still in
  // flight when the list stops containing this slug. Without this the dead end
  // below flashes for the length of that round-trip.
  const hasBeenSeen = useWorkspaceSeen(workspaceSlug, !!workspace);

  const loadingIndicator = (
    <div
      role="status"
      aria-label={tCommon(($) => $.loading)}
      className="flex h-svh items-center justify-center"
    >
      <Sparkles aria-hidden="true" className="size-5 animate-pulse text-muted-foreground" />
    </div>
  );

  if (isAuthLoading) return loadingIndicator;
  // Don't render children until the workspace resolves: useWorkspaceId() and
  // every workspace-scoped query throw or send the wrong header without it.
  // The selector returns undefined until the list resolves, including after a
  // failed request; null means an authoritative list does not contain this slug.
  if (workspace === undefined) return loadingIndicator;
  if (workspace === null) {
    // Just removed: hold the screen empty rather than announcing a dead end the
    // relocation below is already leaving.
    if (hasBeenSeen) return null;
    return <WorkspaceUnavailable />;
  }

  return (
    <WorkspaceSlugProvider slug={workspaceSlug}>
      <WorkspaceChrome slug={workspaceSlug} userName={user?.name}>{children}</WorkspaceChrome>
    </WorkspaceSlugProvider>
  );
}
