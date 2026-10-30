"use client";

import { useId, useMemo, useRef, useState } from "react";
import type { FormEvent, HTMLAttributes } from "react";
import { useQuery } from "@tanstack/react-query";
import { Cloud, Loader2, RefreshCw, Rocket } from "@multica/ui/icons";
import { toast } from "sonner";
import type {
  CloudRuntimeNode,
  CloudRuntimeCapabilities,
} from "@multica/core/runtimes";
import {
  cloudRuntimeNodeListOptions,
  useCreateCloudRuntimeNode,
  cloudRuntimeCapabilityOptions,
  EMPTY_CLOUD_RUNTIME_CAPABILITIES,
  supportsNodeAction,
} from "@multica/core/runtimes";
import { useWorkspaceId } from "@multica/core/hooks";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { CloudNodeActions, NodeActionError } from "./cloud-node-actions";

const CLOUD_RUNTIME_INSTANCE_TYPES = ["t4g.medium", "t4g.large"] as const;
const DEFAULT_INSTANCE_TYPE = CLOUD_RUNTIME_INSTANCE_TYPES[0];
const DEFAULT_DISK_SIZE_GB = 20;

interface CloudRuntimeDialogProps {
  onClose: () => void;
  wsId?: string;
  nodeId?: string;
}

export function CloudRuntimeDialog(props: CloudRuntimeDialogProps) {
  return props.wsId !== undefined ? (
    <CloudRuntimeManager {...props} wsId={props.wsId} />
  ) : (
    <WorkspaceCloudRuntimeManager {...props} />
  );
}

function WorkspaceCloudRuntimeManager(props: CloudRuntimeDialogProps) {
  const wsId = useWorkspaceId();
  return <CloudRuntimeManager {...props} wsId={wsId} />;
}

function CloudRuntimeManager({
  onClose,
  wsId,
  nodeId,
}: CloudRuntimeDialogProps & { wsId: string }) {
  const { t } = useT("runtimes");
  const idPrefix = `cloud-runtime-${useId().replace(/:/g, "")}`;
  const formId = `${idPrefix}-form`;
  const [name, setName] = useState("");
  const [instanceType, setInstanceType] = useState<string>(
    DEFAULT_INSTANCE_TYPE,
  );
  const [diskSizeGB, setDiskSizeGB] = useState(String(DEFAULT_DISK_SIZE_GB));
  const [spec, setSpec] = useState("");
  const [error, setError] = useState<unknown>(null);
  const inFlight = useRef(false);
  const pendingNodes = useRef(new Set<string>());
  const [nodeActionPending, setNodeActionPending] = useState(false);
  const onNodePendingChange = (nodeId: string, pending: boolean) => {
    if (pending) pendingNodes.current.add(nodeId);
    else pendingNodes.current.delete(nodeId);
    setNodeActionPending(pendingNodes.current.size > 0);
  };
  const refreshing = useRef(false);
  const intent = useRef<{ fingerprint: string; key: string } | null>(null);
  const capabilityQuery = useQuery(cloudRuntimeCapabilityOptions(wsId));
  const capabilities = capabilityQuery.isError
    ? EMPTY_CLOUD_RUNTIME_CAPABILITIES
    : (capabilityQuery.data ?? EMPTY_CLOUD_RUNTIME_CAPABILITIES);
  const local = capabilities.provider === "docker";
  const approvedSpec =
    capabilities.specs.find((choice) => choice.id === spec)?.id ??
    capabilities.specs[0]?.id ??
    "";
  const canCreate =
    supportsNodeAction(capabilities, "create") &&
    (!local || approvedSpec !== "");

  const nodesQuery = useQuery(
    cloudRuntimeNodeListOptions(wsId, { limit: 20, offset: 0 }),
  );
  const createNode = useCreateCloudRuntimeNode(wsId);
  const refreshPending = nodesQuery.isFetching || capabilityQuery.isFetching;
  async function refresh() {
    if (
      refreshing.current ||
      refreshPending ||
      inFlight.current ||
      pendingNodes.current.size > 0
    )
      return;
    refreshing.current = true;
    try {
      await Promise.all([nodesQuery.refetch(), capabilityQuery.refetch()]);
    } finally {
      refreshing.current = false;
    }
  }

  const sortedNodes = useMemo(
    () =>
      (nodesQuery.data ?? []).toSorted(
        (a, b) =>
          new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
      ),
    [nodesQuery.data],
  );

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (inFlight.current || !canCreate) return;
    const diskSize = diskSizeGB.trim()
      ? Number(diskSizeGB.trim())
      : DEFAULT_DISK_SIZE_GB;
    if (!local && (!Number.isInteger(diskSize) || diskSize <= 0)) {
      toast.error(t(($) => $.cloud_runtime.validation.disk_size_invalid));
      return;
    }

    const data = local
      ? { name: valueOrUndefined(name), spec: approvedSpec }
      : {
          instance_type: instanceType,
          name: valueOrUndefined(name),
          disk_size_gb: diskSize,
        };
    const fingerprint = JSON.stringify(data);
    if (intent.current?.fingerprint !== fingerprint)
      intent.current = { fingerprint, key: crypto.randomUUID() };
    inFlight.current = true;
    setError(null);
    try {
      await createNode.mutateAsync({
        ...data,
        idempotencyKey: intent.current.key,
      });
      intent.current = null;
      toast.success(t(($) => $.cloud_runtime.toast_created));
      setName("");
      setInstanceType(DEFAULT_INSTANCE_TYPE);
      setDiskSizeGB(String(DEFAULT_DISK_SIZE_GB));
      setSpec("");
    } catch (error) {
      setError(error);
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.cloud_runtime.toast_create_failed),
      );
    } finally {
      inFlight.current = false;
    }
  };

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !inFlight.current && pendingNodes.current.size === 0)
          onClose();
      }}
    >
      <DialogContent
        showCloseButton={false}
        className="flex max-h-[88vh] flex-col gap-0 p-0 sm:max-w-3xl"
      >
        <DialogHeader className="border-b px-6 py-5">
          <DialogTitle className="flex items-center gap-2 text-title-sm">
            <Cloud className="h-4 w-4 text-muted-foreground" />
            {t(($) => $.cloud_runtime.title)}
          </DialogTitle>
        </DialogHeader>

        <div className="min-h-0 flex-1 overflow-y-auto px-6 py-5">
          <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_minmax(280px,0.82fr)]">
            <form id={formId} onSubmit={handleSubmit} className="space-y-4">
              <div>
                <h3 className="text-body font-medium">
                  {t(($) => $.cloud_runtime.create_title)}
                </h3>
              </div>

              <fieldset
                disabled={createNode.isPending}
                className="grid gap-3 sm:grid-cols-2"
              >
                <LabeledInput
                  id={`${idPrefix}-name`}
                  label={t(($) => $.cloud_runtime.fields.name)}
                  value={name}
                  onChange={(value) => {
                    intent.current = null;
                    setName(value);
                  }}
                  placeholder={t(($) => $.cloud_runtime.placeholders.name)}
                />
                {local ? (
                  <LabeledInput
                    id={`${idPrefix}-spec`}
                    label={t(($) => $.cloud_runtime.fields.spec)}
                    value={approvedSpec}
                    onChange={(value) => {
                      intent.current = null;
                      setSpec(value);
                    }}
                    options={capabilities.specs.map((choice) => choice.id)}
                  />
                ) : capabilities.provider === "cloud" ? (
                  <>
                    <LabeledInput
                      id={`${idPrefix}-instance-type`}
                      label={t(($) => $.cloud_runtime.fields.instance_type)}
                      value={instanceType}
                      onChange={(value) => {
                        intent.current = null;
                        setInstanceType(value);
                      }}
                      options={CLOUD_RUNTIME_INSTANCE_TYPES}
                    />
                    <LabeledInput
                      id={`${idPrefix}-disk-size`}
                      label={t(($) => $.cloud_runtime.fields.disk_size)}
                      value={diskSizeGB}
                      onChange={(value) => {
                        intent.current = null;
                        setDiskSizeGB(value);
                      }}
                      placeholder={String(DEFAULT_DISK_SIZE_GB)}
                      type="number"
                      inputMode="numeric"
                    />
                  </>
                ) : null}
              </fieldset>
              {!canCreate && (
                <p className="text-caption text-warning">
                  {t(($) => $.cloud_runtime.capabilities_unavailable)}
                </p>
              )}
              {error !== null && <NodeActionError error={error} />}
            </form>

            <section className="min-h-0 rounded-md border bg-muted/20">
              <div className="flex items-center justify-between border-b bg-background px-3 py-2.5">
                <h3 className="text-body font-medium">
                  {t(($) => $.cloud_runtime.nodes_title)}
                </h3>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => void refresh()}
                  aria-busy={refreshPending}
                  disabled={
                    refreshPending || createNode.isPending || nodeActionPending
                  }
                >
                  <RefreshCw
                    className={cn(
                      "h-3.5 w-3.5",
                      refreshPending && "animate-spin",
                    )}
                  />
                  {t(($) => $.cloud_runtime.refresh)}
                </Button>
              </div>

              {nodesQuery.isError && (
                <div
                  role="alert"
                  className="space-y-1 px-3 py-2 text-caption text-warning"
                >
                  <p>{t(($) => $.cloud_runtime.nodes_failed)}</p>
                  <p>{t(($) => $.cloud_runtime.nodes_failed_hint)}</p>
                </div>
              )}
              {nodesQuery.isLoading ? (
                <div className="flex h-40 items-center justify-center">
                  <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
                </div>
              ) : nodesQuery.isError &&
                nodesQuery.data === undefined ? null : sortedNodes.length ===
                0 ? (
                <div className="flex h-40 flex-col items-center justify-center px-5 text-center">
                  <Cloud className="h-7 w-7 text-faint-foreground" />
                  <p className="mt-3 text-body font-medium">
                    {t(($) => $.cloud_runtime.nodes_empty)}
                  </p>
                </div>
              ) : (
                <div className="max-h-[410px] overflow-y-auto p-2">
                  <div className="space-y-2">
                    {sortedNodes.map((node) => (
                      <CloudRuntimeNodeRow
                        key={node.id}
                        node={node}
                        wsId={wsId}
                        capabilities={capabilities}
                        selected={node.id === nodeId}
                        stale={nodesQuery.isError}
                        onPendingChange={(pending) =>
                          onNodePendingChange(node.id, pending)
                        }
                      />
                    ))}
                  </div>
                </div>
              )}
            </section>
          </div>
        </div>

        <DialogFooter className="m-0 border-t bg-muted/30 px-6 py-3">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={createNode.isPending || nodeActionPending}
            onClick={onClose}
          >
            {t(($) => $.cloud_runtime.cancel)}
          </Button>
          <Button
            type="submit"
            size="sm"
            form={formId}
            disabled={createNode.isPending || !canCreate}
            aria-busy={createNode.isPending}
          >
            {createNode.isPending ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Rocket className="h-3.5 w-3.5" />
            )}
            {t(($) => $.cloud_runtime.create)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function LabeledInput({
  id,
  label,
  value,
  onChange,
  placeholder,
  required,
  type = "text",
  inputMode,
  options,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  required?: boolean;
  type?: string;
  inputMode?: HTMLAttributes<HTMLInputElement>["inputMode"];
  options?: readonly string[];
}) {
  if (options) {
    return (
      <div className="space-y-1.5">
        <Label htmlFor={id} className="text-caption text-muted-foreground">
          {label}
        </Label>
        <Select
          items={options.map((option) => ({ value: option, label: option }))}
          value={value}
          onValueChange={(next) => onChange(next ?? value)}
        >
          <SelectTrigger id={id} className="h-9 w-full rounded-md text-body">
            <SelectValue>
              {() => <span className="truncate">{value}</span>}
            </SelectValue>
          </SelectTrigger>
          <SelectContent align="start">
            {options.map((option) => (
              <SelectItem key={option} value={option}>
                {option}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    );
  }

  return (
    <div className="space-y-1.5">
      <Label htmlFor={id} className="text-caption text-muted-foreground">
        {label}
      </Label>
      <Input
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
        required={required}
        type={type}
        inputMode={inputMode}
        className="h-9 text-body"
      />
    </div>
  );
}

function CloudRuntimeNodeRow({
  node,
  wsId,
  capabilities,
  selected,
  stale,
  onPendingChange,
}: {
  node: CloudRuntimeNode;
  wsId: string;
  capabilities: CloudRuntimeCapabilities;
  selected: boolean;
  stale: boolean;
  onPendingChange: (pending: boolean) => void;
}) {
  const { t } = useT("runtimes");
  const title =
    node.name.trim() ||
    node.instance_id.trim() ||
    t(($) => $.cloud_runtime.node_fallback_name);
  const unknown = t(($) => $.cloud_runtime.diagnostics.unknown);
  const resources = capabilities.specs.find(
    (spec) => spec.id === node.instance_type,
  );
  const desired =
    typeof node.metadata.desired_state === "string"
      ? node.metadata.desired_state
      : unknown;
  const observed =
    typeof node.metadata.observed_state === "string"
      ? node.metadata.observed_state
      : unknown;
  const created = formatDateTime(node.created_at);
  return (
    <article
      className={cn(
        "min-w-0 space-y-3 rounded-md border bg-background px-3 py-2.5",
        selected && "border-ring ring-1 ring-ring",
      )}
      aria-label={title}
    >
      <div className="flex min-w-0 items-center gap-2">
        <span className="min-w-0 truncate text-body font-medium" title={title}>
          {title}
        </span>
        <CloudRuntimeStatusBadge status={node.status} />
      </div>
      <div className="flex flex-wrap gap-x-2 gap-y-1 text-caption text-muted-foreground">
        <span>{node.instance_type || unknown}</span>
        <span>{node.region || unknown}</span>
        {created && <span>{created}</span>}
      </div>
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-caption">
        <dt className="text-muted-foreground">
          {t(($) => $.cloud_runtime.diagnostics.desired)}
        </dt>
        <dd className="break-words">{desired}</dd>
        <dt className="text-muted-foreground">
          {t(($) => $.cloud_runtime.diagnostics.observed)}
        </dt>
        <dd className="break-words">{observed}</dd>
        <dt className="text-muted-foreground">
          {t(($) => $.cloud_runtime.diagnostics.readiness)}
        </dt>
        <dd>
          {stale
            ? unknown
            : node.ready === true
              ? t(($) => $.cloud_runtime.diagnostics.ready)
              : node.provider === "docker"
                ? t(($) => $.cloud_runtime.diagnostics.not_ready)
                : unknown}
        </dd>
        <dt className="text-muted-foreground">
          {t(($) => $.cloud_runtime.diagnostics.resources)}
        </dt>
        <dd className="break-words">
          {resources
            ? t(($) => $.cloud_runtime.diagnostics.resource_limits, {
                cpus: resources.cpus,
                memory: resources.memoryBytes / 1073741824,
                pids: resources.pids,
              })
            : unknown}
        </dd>
        <dt className="text-muted-foreground">
          {t(($) => $.cloud_runtime.diagnostics.storage)}
        </dt>
        <dd>
          {capabilities.provider === "unknown"
            ? unknown
            : capabilities.persistentStorage
              ? t(($) => $.cloud_runtime.diagnostics.persistent)
              : t(($) => $.cloud_runtime.diagnostics.not_supported)}
        </dd>
        <dt className="text-muted-foreground">
          {t(($) => $.cloud_runtime.diagnostics.disk_quota)}
        </dt>
        <dd>
          {capabilities.provider === "unknown"
            ? unknown
            : capabilities.diskQuotaSupported
              ? t(($) => $.cloud_runtime.diagnostics.supported)
              : t(($) => $.cloud_runtime.diagnostics.not_supported)}
        </dd>
      </dl>
      {node.instance_id && (
        <div
          className="truncate font-mono text-micro text-muted-foreground"
          title={node.instance_id}
        >
          {node.instance_id}
        </div>
      )}
      <CloudNodeActions
        node={node}
        capabilities={capabilities}
        wsId={wsId}
        onPendingChange={onPendingChange}
        onDeleted={() => toast.success(t(($) => $.cloud_runtime.toast_deleted))}
      />
    </article>
  );
}

function CloudRuntimeStatusBadge({ status }: { status: string }) {
  const normalized = status.toLowerCase();
  const active = new Set(["running", "success"]);
  const pending = new Set([
    "launching",
    "pending",
    "starting",
    "stopping",
    "rebooting",
    "terminating",
  ]);
  const failed = new Set(["failed", "terminated", "error"]);
  return (
    <Badge
      variant="secondary"
      className={cn(
        "h-5 rounded-md px-1.5 font-mono text-micro",
        active.has(normalized) && "bg-success/10 text-success",
        pending.has(normalized) && "bg-warning/10 text-warning",
        failed.has(normalized) && "bg-destructive/10 text-destructive",
      )}
    >
      {status || "unknown"}
    </Badge>
  );
}

function valueOrUndefined(value: string): string | undefined {
  const trimmed = value.trim();
  return trimmed ? trimmed : undefined;
}

function formatDateTime(value: string): string | null {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return null;
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}
