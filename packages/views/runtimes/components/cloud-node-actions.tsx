"use client";

import { useRef, useState } from "react";
import { Loader2 } from "@multica/ui/icons";
import { ApiError } from "@multica/core/api";
import {
  isCloudRuntimeNodePending,
  supportsNodeAction,
  useStartCloudRuntimeNode,
  useStopCloudRuntimeNode,
  useRebootCloudRuntimeNode,
  useDeleteCloudRuntimeNode,
  type CloudRuntimeNode,
  type CloudRuntimeCapabilities,
  type NodeAction,
} from "@multica/core/runtimes";
import { Button } from "@multica/ui/components/ui/button";
import {
  AlertDialog,
  AlertDialogTrigger,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
} from "@multica/ui/components/ui/alert-dialog";
import { useT } from "../../i18n";

type LifecycleAction = Exclude<NodeAction, "create">;

export function CloudNodeActions({
  node,
  capabilities,
  wsId,
  onDeleted,
  onPendingChange,
}: {
  node: CloudRuntimeNode;
  capabilities: CloudRuntimeCapabilities;
  wsId: string;
  onDeleted: () => void;
  onPendingChange?: (pending: boolean) => void;
}) {
  const { t } = useT("runtimes");
  const start = useStartCloudRuntimeNode(wsId);
  const stop = useStopCloudRuntimeNode(wsId);
  const reboot = useRebootCloudRuntimeNode(wsId);
  const remove = useDeleteCloudRuntimeNode(wsId);
  const intents = useRef<
    Partial<Record<LifecycleAction, { nodeId: string; key: string }>>
  >({});
  const inFlight = useRef(false);
  const [pending, setPending] = useState<LifecycleAction | null>(null);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const unknown =
    !isCloudRuntimeNodePending(node.status) &&
    !["running", "stopped", "failed", "terminated"].includes(
      node.status.toLowerCase(),
    );
  const actionTarget = node.provider === "docker" ? node.id : node.instance_id;
  const canDelete = supportsNodeAction(capabilities, "delete");
  const unavailable =
    pending !== null ||
    isCloudRuntimeNodePending(node.status) ||
    unknown ||
    !actionTarget.trim();
  const labels = {
    start: t(($) => $.cloud_runtime.start),
    stop: t(($) => $.cloud_runtime.stop),
    reboot: t(($) => $.cloud_runtime.restart),
    delete: t(($) => $.cloud_runtime.delete),
  };

  async function act(action: LifecycleAction) {
    if (
      inFlight.current ||
      unavailable ||
      !supportsNodeAction(capabilities, action)
    )
      return;
    inFlight.current = true;
    setPending(action);
    onPendingChange?.(true);
    setError(null);
    let intent = intents.current[action];
    if (!intent || intent.nodeId !== node.id) {
      intent = { nodeId: node.id, key: crypto.randomUUID() };
      intents.current[action] = intent;
    }
    try {
      const variables = {
        instanceId: actionTarget,
        idempotencyKey: intent.key,
      };
      if (action === "start") await start.mutateAsync(variables);
      else if (action === "stop") await stop.mutateAsync(variables);
      else if (action === "reboot") await reboot.mutateAsync(variables);
      else await remove.mutateAsync(variables);
      delete intents.current[action];
      if (action === "delete") {
        setDeleteOpen(false);
        onDeleted();
      }
    } catch (failure) {
      setError(failure);
    } finally {
      inFlight.current = false;
      setPending(null);
      onPendingChange?.(false);
    }
  }

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-2">
        {(["start", "stop", "reboot"] as const)
          .filter((action) => supportsNodeAction(capabilities, action))
          .map((action) => (
            <Button
              key={action}
              type="button"
              variant="outline"
              size="sm"
              disabled={unavailable}
              aria-busy={pending === action}
              onClick={() => void act(action)}
            >
              {pending === action && (
                <Loader2 aria-hidden className="size-3.5 animate-spin" />
              )}
              {labels[action]}
            </Button>
          ))}
        {(canDelete || deleteOpen) && (
          <AlertDialog
            open={deleteOpen}
            onOpenChange={(open) => {
              if (!inFlight.current) setDeleteOpen(open);
            }}
          >
            <AlertDialogTrigger
              render={
                <Button
                  type="button"
                  variant="destructive"
                  size="sm"
                  disabled={unavailable || !canDelete}
                />
              }
            >
              {labels.delete}
            </AlertDialogTrigger>
            <AlertDialogContent className="max-h-[88vh] overflow-y-auto">
              <AlertDialogHeader>
                <AlertDialogTitle>{labels.delete}</AlertDialogTitle>
                <AlertDialogDescription className="text-warning">
                  {t(($) => $.cloud_runtime.delete_warning)}
                </AlertDialogDescription>
              </AlertDialogHeader>
              <p className="break-words text-body font-medium">
                {node.name || node.instance_id}
              </p>
              {error !== null && <NodeActionError error={error} />}
              <AlertDialogFooter>
                <Button
                  type="button"
                  variant="outline"
                  disabled={pending !== null}
                  onClick={() => setDeleteOpen(false)}
                >
                  {t(($) => $.cloud_runtime.cancel)}
                </Button>
                <Button
                  type="button"
                  variant="destructive"
                  disabled={unavailable || !canDelete}
                  aria-busy={pending === "delete"}
                  onClick={() => void act("delete")}
                >
                  {pending === "delete" && (
                    <Loader2 aria-hidden className="size-3.5 animate-spin" />
                  )}
                  {labels.delete}
                </Button>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        )}
      </div>
      {capabilities.provider === "unknown" && (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.cloud_runtime.capabilities_unavailable)}
        </p>
      )}
      {(unknown ||
        (!actionTarget.trim() && !isCloudRuntimeNodePending(node.status))) && (
        <p className="text-caption text-warning">
          {t(($) => $.cloud_runtime.recovery.unknown)}
        </p>
      )}
      {error !== null && !deleteOpen && <NodeActionError error={error} />}
      {node.errorCode && error === null && (
        <NodeActionError error={node.errorCode} />
      )}
    </div>
  );
}

/** Public error codes describe recovery, never credential or provider health. */
export function NodeActionError({ error }: { error: unknown }) {
  const { t } = useT("runtimes");
  let code =
    typeof error === "string"
      ? error
      : error instanceof Error
        ? error.message
        : "";
  if (
    error instanceof ApiError &&
    error.body &&
    typeof error.body === "object" &&
    "error_code" in error.body &&
    typeof error.body.error_code === "string"
  )
    code = error.body.error_code;
  const message =
    code === "busy"
      ? t(($) => $.cloud_runtime.recovery.busy)
      : code === "profile_missing"
        ? t(($) => $.cloud_runtime.recovery.profile_missing)
        : [
              "unknown_health",
              "provider_unavailable",
              "unavailable",
              "instance_missing",
            ].includes(code)
          ? t(($) => $.cloud_runtime.recovery.unknown)
          : t(($) => $.cloud_runtime.recovery.retry);
  return (
    <p role="alert" className="text-caption text-warning">
      {message}
    </p>
  );
}
