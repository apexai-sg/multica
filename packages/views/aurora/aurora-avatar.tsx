"use client";

import { cn } from "@multica/ui/lib/utils";
import { avatarInitial, avatarTint } from "./aurora-avatar-helpers";

export {
  avatarInitial,
  avatarTint,
  tintIndex,
  type AuroraAvatarTint,
} from "./aurora-avatar-helpers";

export function AuroraAvatar({
  name,
  id,
  size = "md",
  available = false,
  className,
}: {
  name: string;
  id: string;
  size?: "md" | "lg";
  /** Draws the state dot. Only a runnable skill gets one. */
  available?: boolean;
  className?: string;
}) {
  // Decorative in every call site: the tile always sits beside the name it
  // stands for, so announcing it would read the identity twice.
  return (
    <span
      aria-hidden="true"
      className={cn(
        "aurora-avatar",
        size === "lg" && "aurora-avatar--lg",
        "aurora-avatar--" + avatarTint(id),
        className,
      )}
    >
      {avatarInitial(name)}
      {available ? <span className="aurora-avatar-dot" /> : null}
    </span>
  );
}
