"use client";

import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "../auth";
import type { MemberRole, MemberWithUser } from "../types";
import { memberListOptions } from "../workspace/queries";

/**
 * Resolves the current user's membership in the given workspace. Single source
 * of truth for "what role am I" — replaces ad-hoc `members.find(...)` lookups
 * scattered across the views.
 *
 * `wsId` is explicit (not via `useWorkspaceId()` Context) so this hook stays
 * usable in components that may render before workspace context is wired,
 * matching the repo rule for workspace-aware hooks.
 *
 * `isError` and `refetch` are exposed for callers that must tell "still loading"
 * from "the member list failed" and offer a retry; callers that only need the
 * role can keep destructuring the three original fields.
 */
function isMemberRole(value: string | undefined): value is MemberRole {
  return value === "owner" || value === "admin" || value === "member";
}

export function useCurrentMember(wsId: string): {
  userId: string | null;
  role: MemberRole | null;
  member: MemberWithUser | null;
  isLoading: boolean;
  isError: boolean;
  isFetched: boolean;
  refetch: () => void;
} {
  const userId = useAuthStore((s) => s.user?.id ?? null);
  const { data: members, isLoading, isError, isFetched, refetch } = useQuery(
    memberListOptions(wsId),
  );
  // TanStack binds refetch once per observer, so it keeps the same identity for
  // as long as this hook is mounted. Return it directly: wrapping it would only
  // reproduce an identity that already holds.
  const member = members?.find((m) => m.user_id === userId) ?? null;
  return {
    userId,
    // Fail closed: a role this build does not know is treated as "no role",
    // which every permission rule denies. The wire type says MemberRole, but a
    // newer server can send a value this build predates.
    role: isMemberRole(member?.role) ? member.role : null,
    member,
    isLoading,
    isError,
    isFetched,
    refetch,
  };
}
