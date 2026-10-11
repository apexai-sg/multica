"use client";

import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
  CardContent,
} from "@multica/ui/components/ui/card";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "@multica/views/i18n";
import { useLogout } from "@multica/views/auth";

/**
 * Shown after a successful sign-in that left the account without a workspace.
 *
 * Registration opens a personal workspace server-side on every login and the
 * attempt is idempotent, so this is a state that resolves itself on the next
 * sign-in rather than one the user configures their way out of. Aurora serves
 * no workspace-creation route — the account model is one personal space,
 * decided by the server — so signing in again is the whole recovery, and the
 * button does exactly that instead of offering a form that would be a second,
 * divergent way to create the same thing.
 */
export function NoWorkspaceNotice() {
  const { t } = useT("aurora");
  const { t: tLayout } = useT("layout");
  const logout = useLogout();

  return (
    <div className="aurora-centered">
      <Card className="w-full max-w-sm">
        <CardHeader className="text-center">
          <CardTitle className="text-display-sm">
            {t(($) => $.workspace.missing_title)}
          </CardTitle>
          <CardDescription>
            {t(($) => $.workspace.missing_description)}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex justify-center">
          <Button type="button" variant="outline" onClick={logout}>
            {tLayout(($) => $.sidebar.log_out)}
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}
