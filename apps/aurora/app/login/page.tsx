"use client";

import { Suspense, useEffect, useRef, useState } from "react";
import { useSearchParams, useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { sanitizeNextUrl, useAuthStore } from "@multica/core/auth";
import { useConfigStore } from "@multica/core/config";
import {
  workspaceKeys,
  workspaceListOptions,
} from "@multica/core/workspace/queries";
import type { Workspace } from "@multica/core/types";
import { LoginPage, beginGoogleOAuthFlow } from "@multica/views/auth";
import { useT } from "@multica/views/i18n";
import { setLoggedInCookie } from "@multica/nextjs/auth-cookie";
import { resolveAuroraDestination } from "@/lib/routes";
import { NoWorkspaceNotice } from "@/components/no-workspace-notice";

function LoginPageContent() {
  const { t } = useT("aurora");
  const router = useRouter();
  const qc = useQueryClient();
  const googleClientId = useConfigStore((state) => state.googleClientId);
  const user = useAuthStore((s) => s.user);
  const isLoading = useAuthStore((s) => s.isLoading);
  const searchParams = useSearchParams();
  const [noWorkspace, setNoWorkspace] = useState(false);

  // `next` carries the protected URL the user was originally headed to. Aurora
  // only ever bounces here from a workspace route, so the deep link is a
  // workspace path — sanitize anyway, since the parameter is user-writable.
  const nextUrl = sanitizeNextUrl(searchParams.get("next"));

  // Latched once auth has been observed settled as signed-out on this page.
  // Any `user` that appears afterwards came from the form in this session, so
  // handleSuccess owns the navigation and this effect must not race it by
  // reading a workspace list that has not been seeded yet.
  const settledLoggedOutRef = useRef(false);

  // Send an already-signed-in visitor on. Fetch rather than read the cache: on
  // a fresh page load the cache is cold, and `getQueryData() ?? []` would
  // report "no workspace" to a user who has one. A failed fetch falls back to
  // [] — the same answer the cold read gave — rather than trapping the user
  // here on a network blip.
  useEffect(() => {
    if (isLoading) return;
    if (!user) {
      settledLoggedOutRef.current = true;
      return;
    }
    if (settledLoggedOutRef.current) return;
    if (nextUrl) {
      router.replace(nextUrl);
      return;
    }
    void qc
      .ensureQueryData(workspaceListOptions())
      .catch(() => [] as Workspace[])
      .then((list) => {
        const destination = resolveAuroraDestination(list);
        if (destination) router.replace(destination);
        else setNoWorkspace(true);
      });
  }, [isLoading, user, router, nextUrl, qc]);

  const handleSuccess = () => {
    if (nextUrl) {
      router.push(nextUrl);
      return;
    }
    // The list was seeded into the cache by the login flow before this runs.
    const list = qc.getQueryData<Workspace[]>(workspaceKeys.list()) ?? [];
    const destination = resolveAuroraDestination(list);
    if (destination) router.push(destination);
    else setNoWorkspace(true);
  };

  if (noWorkspace) return <NoWorkspaceNotice />;

  return (
    <LoginPage
      logo={
        <span className="aurora-brand aurora-brand--on-light justify-center">
          <span aria-hidden="true" className="aurora-brand-mark">A</span>
          <span className="aurora-brand-copy">
            <strong>Aurora</strong>
            <small>{t(($) => $.shell.brand_tagline)}</small>
          </span>
        </span>
      }
      onSuccess={handleSuccess}
      google={
        googleClientId
          ? {
              clientId: googleClientId,
              redirectUri: `${window.location.origin}/auth/callback`,
              // `next` has to survive the OAuth round-trip, and `state` is the
              // only channel that does. `beginGoogleOAuthFlow` prepends the
              // per-flow CSRF nonce the callback compares against before it
              // exchanges the code.
              state: () =>
                beginGoogleOAuthFlow(nextUrl ? [`next:${nextUrl}`] : []),
            }
          : undefined
      }
      onTokenObtained={setLoggedInCookie}
    />
  );
}

export default function Page() {
  return (
    <Suspense fallback={null}>
      <LoginPageContent />
    </Suspense>
  );
}
