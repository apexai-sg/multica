"use client";

import { useState } from "react";
import { parseAvatarEmoji } from "@multica/ui/lib/avatar-emoji";
import { cn } from "@multica/ui/lib/utils";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { avatarInitial, avatarTint } from "./aurora-avatar-helpers";

export {
  avatarInitial,
  avatarTint,
  tintIndex,
  type AuroraAvatarTint,
} from "./aurora-avatar-helpers";

/**
 * The design's avatar: the carrier agent's portrait when the workspace has one,
 * initials on a pastel tile when it does not.
 *
 * The portrait is whatever a member set on the skill's carrier agent in the
 * Agents settings, so the directory shows the same face that agent wears
 * everywhere else in Multica. The tint stays derived from the id (see
 * ./aurora-avatar-helpers): it is the fallback identity, and it must not change
 * as a portrait comes and goes.
 */
export function AuroraAvatar({
  name,
  id,
  avatarUrl,
  size = "md",
  available = false,
  className,
}: {
  name: string;
  id: string;
  /**
   * The carrier agent's avatar, or an `emoji:` marker. Null, undefined and ""
   * all mean "no portrait" and draw the initials.
   */
  avatarUrl?: string | null;
  size?: "md" | "lg";
  /** Draws the state dot. Only a runnable skill gets one. */
  available?: boolean;
  className?: string;
}) {
  // The failure is remembered as the URL that failed rather than as a boolean:
  // a skill can be re-pointed at another carrier between renders, and a new
  // portrait must not stay hidden behind the previous one's error. Keeping the
  // URL makes that fall out of the comparison instead of needing a reset effect.
  const [failedUrl, setFailedUrl] = useState<string | null>(null);
  const emoji = parseAvatarEmoji(avatarUrl);
  // A relative path is one of the backend's own objects (`/uploads/...`), so
  // resolvePublicFileUrl anchors it on the API base; an absolute URL passes
  // through untouched. "No portrait" never reaches the API client at all, so a
  // skill without one cannot depend on it being initialised.
  const resolvedUrl = !emoji && avatarUrl ? resolvePublicFileUrl(avatarUrl) : null;
  const imageUrl =
    resolvedUrl !== null && resolvedUrl !== failedUrl ? resolvedUrl : null;

  // Decorative in every call site: the tile always sits beside the name it
  // stands for, so announcing it would read the identity twice.
  return (
    <span
      aria-hidden="true"
      className={cn(
        "aurora-avatar",
        size === "lg" && "aurora-avatar--lg",
        // The tint paints the fallback, not the frame: a portrait covers it.
        !emoji && imageUrl === null && "aurora-avatar--" + avatarTint(id),
        className,
      )}
    >
      {emoji ? (
        <span className="aurora-avatar-emoji">{emoji}</span>
      ) : imageUrl ? (
        <img
          src={imageUrl}
          alt=""
          className="aurora-avatar-img"
          onError={() => setFailedUrl(imageUrl)}
        />
      ) : (
        avatarInitial(name)
      )}
      {available ? <span className="aurora-avatar-dot" /> : null}
    </span>
  );
}
