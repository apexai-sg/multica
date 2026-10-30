"use client";

import { Frown } from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";
import { AuroraPageState } from "./aurora-page-header";
import { useT } from "../i18n";

/**
 * "This screen could not be read, and here is the way back."
 *
 * The three Aurora surfaces each fail the same way — one or more of their reads
 * errored before there was anything to show — so the state they render is one
 * component rather than three copies free to drift apart in tone, role or
 * recovery. Only the title differs, because only the title names what failed.
 *
 * A failed read over data the screen already has is *not* this state: the
 * caller keeps rendering what it read and says nothing here, since replacing a
 * filled screen with an error is a worse trade than showing it stale.
 */
export function AuroraLoadFailed({
  title,
  onRetry,
}: {
  title: string;
  onRetry: () => void;
}) {
  const { t } = useT("aurora");
  return (
    <AuroraPageState
      icon={Frown}
      tone="destructive"
      role="alert"
      title={title}
      actions={
        <Button type="button" variant="outline" size="sm" onClick={onRetry}>
          {t(($) => $.retry)}
        </Button>
      }
    />
  );
}
