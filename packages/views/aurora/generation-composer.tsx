"use client";

import {
  useRef,
  useState,
  type ChangeEvent,
  type DragEvent,
  type FormEvent,
} from "react";
import {
  CircleAlert,
  CreditCard,
  FileDown,
  Sparkles,
  Upload,
  X,
} from "lucide-react";
import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
} from "@multica/ui/components/ui/alert";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { toolGuidance } from "./tool-guidance";
import { Label } from "@multica/ui/components/ui/label";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@multica/ui/components/ui/sheet";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { Textarea } from "@multica/ui/components/ui/textarea";
import {
  isAuroraDegraded,
  isAuroraGenerationTerminal,
  isAuroraInsufficientCreditsError,
  isAuroraRateLimitError,
  useAuroraBalance,
  useAuroraGenerationDetail,
  useCreateAuroraGeneration,
  type AuroraSkill,
} from "@multica/core/aurora";
import { api } from "@multica/core/api";
import { useFileUpload } from "@multica/core/hooks/use-file-upload";
import { AppLink } from "../navigation";
import { useLocale, useT } from "../i18n";
import { formatCredits, formatMicroCredits } from "./format";
import {
  auroraCategoryLabel,
  generationStatusLabel,
  skillDisplayName,
} from "./labels";
import { AuroraAvatar } from "./aurora-avatar";
import { GENERATION_STATUS_PILL } from "./generation-artifacts";
import { useAuroraAssetDownload } from "./use-aurora-asset-download";

// The attachment kinds the composer can accept and the tokens the file dialog
// filters on. Both derive from the parsed policy, so a new server-side rule
// needs no client change. Document MIMEs are not listed in `accept` because
// browsers disagree about `.md`; the extension tokens cover them.
const ACCEPT_BY_KIND: Record<string, string[]> = {
  image: ["image/png", "image/jpeg"],
  document: [".txt", ".md", ".pdf", ".docx"],
  audio: [".wav", ".mp3", ".ogg", ".opus"],
  video: [".mp4", ".mov", ".webm"],
};

const DOCUMENT_MIMES = new Set([
  "application/pdf",
  "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
]);

function acceptForKinds(kinds: string[]): string {
  const tokens: string[] = [];
  for (const kind of kinds) {
    for (const token of ACCEPT_BY_KIND[kind] ?? []) {
      if (!tokens.includes(token)) tokens.push(token);
    }
  }
  return tokens.join(",");
}

// fileKind classifies a picked file the same way the server classifies a
// stored attachment: by the browser's MIME first, then by extension when the
// MIME is generic. Undefined means the policy has no rule for it.
function fileKind(file: File): string | undefined {
  const type = file.type.toLowerCase();
  if (type.startsWith("image/")) return "image";
  if (type.startsWith("audio/")) return "audio";
  if (type.startsWith("video/")) return "video";
  if (type.startsWith("text/") || DOCUMENT_MIMES.has(type)) return "document";

  const name = file.name.toLowerCase();
  if (/\.(png|jpe?g)$/.test(name)) return "image";
  if (/\.(md|markdown|txt|pdf|docx)$/.test(name)) return "document";
  if (/\.(wav|mp3|ogg|opus)$/.test(name)) return "audio";
  if (/\.(mp4|mov|webm)$/.test(name)) return "video";
  return undefined;
}

function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

type AttachmentFailure = "unsupported" | "too_large" | "upload";

/** One file the user picked, from selection through upload to submit. */
interface PendingAttachment {
  key: number;
  name: string;
  size: number;
  status: "uploading" | "uploaded" | "failed";
  attachmentId?: string;
  failure?: AttachmentFailure;
}

/**
 * The task drawer: turn one skill into one generation and follow it to its
 * result.
 *
 * The drawer is opened from the skill grid rather than routed to, so it takes
 * the skill it runs as a prop and reports dismissal back up — the grid owns
 * which skill is selected, exactly as the platform owns the URL. `worksHref`
 * is supplied by the host app for the same reason `paths` is not imported here:
 * the library route belongs to the app that defines it.
 */
export interface GenerationComposerProps {
  /** The skill to run, or null while no drawer is open. */
  skill: AuroraSkill | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The app's library route. Omitted, the result offers no link. */
  worksHref?: string;
  /** The app's checkout route. Omitted, the shortfall offers no way to top up. */
  topUpHref?: string;
  /**
   * The app's route for the issue a generation runs on. Takes the
   * `issueIdentifier` and returns null when it cannot build a URL — what core's
   * `auroraIssueHref` does for a missing or degraded identifier — in which case
   * the drawer keeps polling the generation instead.
   */
  issueHref?: (identifier: string) => string | null;
}

export function GenerationComposer({
  skill,
  open,
  onOpenChange,
  worksHref,
  topUpHref,
  issueHref,
}: GenerationComposerProps) {
  if (!skill) return null;
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="aurora-drawer">
        {/* Keyed on the skill so switching skills remounts the body: the draft,
            the in-flight generation and any failure all belong to the skill
            that was open, and none of them should survive into another's. */}
        <ComposerBody
          key={skill.id}
          skill={skill}
          worksHref={worksHref}
          topUpHref={topUpHref}
          issueHref={issueHref}
        />
      </SheetContent>
    </Sheet>
  );
}

/**
 * What became of the one submit this drawer allows.
 *
 * Modelled as a state rather than three loose values so "no generation yet" and
 * "the create call failed" cannot both be true — the status badge and the error
 * alert are two renderings of the same question.
 */
type SubmitOutcome =
  | { kind: "idle" }
  | { kind: "started"; generationId: string }
  | { kind: "failed"; error: unknown }
  /**
   * The create call answered 2xx with a body that could not be read, so the
   * server may well have enqueued the generation and reserved its credits. It
   * is not a failure — there is simply no id to follow — which is why it is not
   * the generic `failed`: telling the user to retry here is how a double charge
   * happens. The copy sends them to the library instead, and the submit button
   * is spent with it, so the drawer cannot say one thing and do another.
   */
  | { kind: "unreadable" };

function ComposerBody({
  skill,
  worksHref,
  topUpHref,
  issueHref,
}: {
  skill: AuroraSkill;
  worksHref?: string;
  topUpHref?: string;
  issueHref?: (identifier: string) => string | null;
}) {
  const { t } = useT("aurora");
  const download = useAuroraAssetDownload();
  const locale = useLocale();
  const create = useCreateAuroraGeneration();
  const { upload } = useFileUpload(api);
  const [prompt, setPrompt] = useState("");
  const [audience, setAudience] = useState("");
  const [preferences, setPreferences] = useState("");
  const guidance = toolGuidance(skill.id, locale);
  const [outcome, setOutcome] = useState<SubmitOutcome>({ kind: "idle" });
  const [attachments, setAttachments] = useState<PendingAttachment[]>([]);
  const nextAttachmentKey = useRef(0);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [draggingFiles, setDraggingFiles] = useState(false);

  const generationId = outcome.kind === "started" ? outcome.generationId : "";
  const detail = useAuroraGenerationDetail(generationId);
  const generation = detail.data?.value;
  // A body the schema rejected resolves the query without a generation to show,
  // so it is "cannot see it right now" for the same reason a failed read is —
  // and not, as it would render otherwise, a progress line that never moves.
  const progressUnreadable = detail.isError || isAuroraDegraded(detail.data);

  // The input policy comes from the parsed catalog response, never a second
  // skill-id switch: the accept filter, the required count, and the per-file
  // cap all derive from the same rules the server validates against.
  const attachmentRules = skill.attachments ?? [];
  const attachmentKinds = Array.from(
    new Set(attachmentRules.flatMap((rule) => rule.kinds)),
  );
  const attachmentKindKey = [...attachmentKinds].sort().join("+");
  const attachmentLabel =
    attachmentKindKey === "document"
      ? t(($) => $.composer.attachments_label_document)
      : attachmentKindKey === "audio"
        ? t(($) => $.composer.attachments_label_audio)
        : attachmentKindKey === "video"
          ? t(($) => $.composer.attachments_label_video)
          : attachmentKindKey === "audio+video"
            ? t(($) => $.composer.attachments_label_audio_video)
            : t(($) => $.composer.attachments_label_image);
  const accept = acceptForKinds(attachmentKinds);
  // Named from the same tokens the file dialog filters on, so the hint cannot
  // drift from the policy the server enforces.
  const acceptedFormats = accept
    .split(",")
    .map((token) =>
      (token.startsWith(".") ? token.slice(1) : token.replace(/^image\//, "")).toUpperCase(),
    )
    .join(", ");
  const maxAttachments = attachmentRules.reduce(
    (sum, rule) => sum + rule.max,
    0,
  );
  const requiredAttachments = attachmentRules.reduce(
    (sum, rule) => sum + rule.min,
    0,
  );

  const trimmedPrompt = prompt.trim();
  // Only fully uploaded files can be submitted, and only once the policy's
  // minimum is met. A pending or failed upload keeps submit disabled rather
  // than sending a request the server would reject — or silently dropping a
  // file the user believes is part of the generation.
  const uploadedAttachmentIds = attachments.flatMap((item) =>
    item.status === "uploaded" && item.attachmentId ? [item.attachmentId] : [],
  );
  const attachmentsBlocked = attachments.some(
    (item) => item.status !== "uploaded",
  );
  const attachmentsReady =
    !attachmentsBlocked && uploadedAttachmentIds.length >= requiredAttachments;

  // One submission per drawer. A second POST reserves the skill's credits a
  // second time, and the drawer is only showing one generation's progress — so
  // the button is spent once the server has accepted the first one. A *failed*
  // submit stays live, because retrying that costs nothing; an *unreadable* one
  // does not, because the server may have accepted it (see SubmitOutcome) and
  // the copy sends the user to the library before the next attempt.
  const canSubmit =
    skill.available &&
    trimmedPrompt.length > 0 &&
    attachmentsReady &&
    !create.isPending &&
    outcome.kind !== "started" &&
    outcome.kind !== "unreadable";

  // Pick files, upload each through the existing authenticated upload API, and
  // keep the prompt and every successful selection on all failure paths.
  function handleAttachmentsPicked(event: ChangeEvent<HTMLInputElement>) {
    const picked = Array.from(event.target.files ?? []);
    // Reset so choosing the same file again still fires a change event.
    event.target.value = "";
    void uploadFiles(picked);
  }

  // The picker and a drop share one path, so the policy check, the per-file
  // failures and the upload call exist once.
  async function uploadFiles(picked: File[]) {
    if (picked.length === 0) return;

    const room = Math.max(0, maxAttachments - attachments.length);
    for (const file of picked.slice(0, room)) {
      const key = nextAttachmentKey.current++;
      const kind = fileKind(file);
      const rule = kind
        ? attachmentRules.find((candidate) => candidate.kinds.includes(kind))
        : undefined;
      if (!kind || !rule) {
        setAttachments((prev) => [
          ...prev,
          {
            key,
            name: file.name,
            size: file.size,
            status: "failed",
            failure: "unsupported",
          },
        ]);
        continue;
      }
      if (rule.maxBytes > 0 && file.size > rule.maxBytes) {
        setAttachments((prev) => [
          ...prev,
          {
            key,
            name: file.name,
            size: file.size,
            status: "failed",
            failure: "too_large",
          },
        ]);
        continue;
      }
      setAttachments((prev) => [
        ...prev,
        { key, name: file.name, size: file.size, status: "uploading" },
      ]);
      try {
        const result = await upload(file);
        setAttachments((prev) =>
          prev.map((item) =>
            item.key === key
              ? result?.id
                ? { ...item, status: "uploaded", attachmentId: result.id }
                : { ...item, status: "failed", failure: "upload" }
              : item,
          ),
        );
      } catch {
        setAttachments((prev) =>
          prev.map((item) =>
            item.key === key
              ? { ...item, status: "failed", failure: "upload" }
              : item,
          ),
        );
      }
    }
  }

  function handleFileDrop(event: DragEvent<HTMLButtonElement>) {
    event.preventDefault();
    setDraggingFiles(false);
    if (!skill.available) return;
    void uploadFiles(Array.from(event.dataTransfer?.files ?? []));
  }

  function removeAttachment(key: number) {
    setAttachments((prev) => prev.filter((item) => item.key !== key));
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canSubmit) return;
    setOutcome({ kind: "idle" });
    try {
      const created = await create.mutateAsync({
        skillId: skill.id,
        prompt: [
          trimmedPrompt,
          audience.trim() ? `${t(($) => $.composer.audience_label)}: ${audience.trim()}` : "",
          preferences.trim() ? `${t(($) => $.composer.preferences_label)}: ${preferences.trim()}` : "",
        ].filter(Boolean).join("\n\n"),
        attachmentIds: uploadedAttachmentIds,
      });
      // A usable issue identifier means the result belongs on the issue detail,
      // not in this drawer: open it and let the drawer stop following the
      // generation. A missing or degraded identifier (the old server's "#N")
      // keeps the polling path below.
      const identifier = created.value?.issueIdentifier ?? "";
      const issueUrl = identifier && issueHref ? issueHref(identifier) : null;
      if (issueUrl) {
        window.location.assign(issueUrl);
        return;
      }
      setOutcome(
        created.value
          ? { kind: "started", generationId: created.value.id }
          : { kind: "unreadable" },
      );
    } catch (error) {
      setOutcome({ kind: "failed", error });
    }
  }

  // Read from the raw status rather than the label: the label has already
  // collapsed an unrecognised status into "unknown", which is not a reason to
  // stop showing that the server is still working.
  const isTerminal = generation
    ? isAuroraGenerationTerminal(generation.status)
    : false;
  const assets = generation?.assets ?? [];
  // A finished generation reports its outcome even when it produced no files —
  // "nothing came back" is a result the user needs to see, not a reason to
  // render nothing.
  const showResult =
    assets.length > 0 ||
    (generation ? generationStatusLabel(generation.status) === "completed" : false);

  return (
    <div className="flex flex-col gap-5">
      <SheetHeader className="aurora-drawer-head flex-row">
        <AuroraAvatar
          name={skillDisplayName(skill, locale)}
          id={skill.id}
          avatarUrl={skill.avatarUrl}
          size="lg"
          available={skill.available}
        />
        <div>
          <span className="aurora-drawer-head-category">
            {t(
              ($) => $.directory.categories[auroraCategoryLabel(skill.category)],
            )}
          </span>
          <SheetTitle>{skillDisplayName(skill, locale)}</SheetTitle>
          <SheetDescription>
            {t(($) => $.composer.cost, {
              credits: formatCredits(skill.credits, locale),
            })}
          </SheetDescription>
        </div>
      </SheetHeader>

      {!skill.available ? (
        <Alert>
          <CircleAlert aria-hidden="true" />
          <AlertTitle>{t(($) => $.composer.unavailable_title)}</AlertTitle>
          <AlertDescription>
            {t(($) => $.composer.unavailable_description)}
          </AlertDescription>
        </Alert>
      ) : null}

      {outcome.kind === "failed" ? (
        <SubmitFailure
          error={outcome.error}
          skill={skill}
          topUpHref={topUpHref}
        />
      ) : null}

      {guidance ? (
        <section className="aurora-deliverable" aria-label={t(($) => $.composer.deliverable_title)}>
          <strong>{t(($) => $.composer.deliverable_title)}</strong>
          <p>{guidance.summary}</p>
          {skill.output.length === 1 && skill.output[0] === "text" ? <span>{t(($) => $.directory.text_output)}</span> : null}
        </section>
      ) : null}
      <form className="aurora-composer" onSubmit={handleSubmit}>
        <div className="aurora-field">
          <Label className="aurora-field-label" htmlFor="aurora-prompt">
            {t(($) => $.composer.prompt_label)}
          </Label>
          <Textarea
            id="aurora-prompt"
            className="aurora-textarea"
            value={prompt}
            onChange={(event) => setPrompt(event.target.value)}
            placeholder={guidance?.example ?? t(($) => $.composer.prompt_placeholder)}
            rows={5}
            // The prompt is the whole input, and losing it costs the user their
            // work — so it is kept rather than cleared when the submit fails.
            disabled={!skill.available}
          />
        </div>
        {guidance ? (
          <>
            <Button type="button" variant="outline" size="sm"
              disabled={!skill.available || prompt.trim().length > 0 || create.isPending || outcome.kind === "started" || outcome.kind === "unreadable"}
              onClick={() => setPrompt(guidance.example)}>
              <Sparkles aria-hidden="true" />{t(($) => $.composer.example)}
            </Button>
            <div className="aurora-field">
              <Label htmlFor="aurora-audience">{t(($) => $.composer.audience_label)}</Label>
              <Input id="aurora-audience" value={audience} onChange={(event) => setAudience(event.target.value)}
                disabled={!skill.available} placeholder={t(($) => $.composer.audience_placeholder)} />
            </div>
            <div className="aurora-field">
              <Label htmlFor="aurora-preferences">{t(($) => $.composer.preferences_label)}</Label>
              <Input id="aurora-preferences" value={preferences} onChange={(event) => setPreferences(event.target.value)}
                disabled={!skill.available} placeholder={t(($) => $.composer.preferences_placeholder)} />
            </div>
          </>
        ) : null}
        {attachmentRules.length > 0 ? (
          <div className="aurora-field">
            <Label className="aurora-field-label" htmlFor="aurora-attachments">
              {attachmentLabel}
            </Label>
            {/* The native control stays in the tree — it owns the accept
                policy and is what the label addresses — while the dropzone in
                front of it is the visible affordance. */}
            <input
              ref={fileInputRef}
              id="aurora-attachments"
              className="aurora-file-input"
              type="file"
              accept={accept}
              multiple={maxAttachments > 1}
              tabIndex={-1}
              disabled={!skill.available}
              onChange={handleAttachmentsPicked}
            />
            <button
              type="button"
              className="aurora-dropzone"
              data-dragging={draggingFiles ? "" : undefined}
              disabled={!skill.available}
              aria-describedby="aurora-attachments-hint"
              onClick={() => fileInputRef.current?.click()}
              onDragEnter={(event) => {
                event.preventDefault();
                if (skill.available) setDraggingFiles(true);
              }}
              onDragOver={(event) => {
                event.preventDefault();
                if (skill.available) setDraggingFiles(true);
              }}
              onDragLeave={() => setDraggingFiles(false)}
              onDrop={handleFileDrop}
            >
              <Upload aria-hidden="true" />
              {t(($) => $.composer.attachments_choose)}
            </button>
            <p className="aurora-field-hint" id="aurora-attachments-hint">
              {t(($) => $.composer.attachments_hint, {
                formats: acceptedFormats,
                max: String(maxAttachments),
              })}
            </p>
            {attachments.length > 0 ? (
              <ul className="aurora-attachment-list">
                {attachments.map((item) => (
                  <li key={item.key} data-status={item.status}>
                    <span className="aurora-attachment-name">{item.name}</span>
                    <span className="aurora-attachment-meta">
                      {formatFileSize(item.size)}
                    </span>
                    <span className="aurora-attachment-meta">
                      {item.status === "uploading"
                        ? t(($) => $.composer.attachment_uploading)
                        : item.status === "uploaded"
                          ? t(($) => $.composer.attachment_uploaded)
                          : item.failure === "unsupported"
                            ? t(($) => $.composer.attachment_unsupported)
                            : item.failure === "too_large"
                              ? t(($) => $.composer.attachment_too_large)
                              : t(($) => $.composer.attachment_upload_failed)}
                    </span>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-xs"
                      aria-label={t(($) => $.composer.attachment_remove, {
                        name: item.name,
                      })}
                      onClick={() => removeAttachment(item.key)}
                    >
                      <X aria-hidden="true" />
                    </Button>
                  </li>
                ))}
              </ul>
            ) : null}
          </div>
        ) : null}
        <div className="aurora-composer-tools">
          <Button type="submit" disabled={!canSubmit} aria-busy={create.isPending}>
            {create.isPending ? <Spinner aria-hidden="true" /> : null}
            {create.isPending
              ? t(($) => $.composer.submitting)
              : t(($) => $.composer.submit)}
          </Button>
        </div>
      </form>

      {outcome.kind === "unreadable" ? (
        <Alert variant="destructive">
          <CircleAlert aria-hidden="true" />
          <AlertDescription>
            {t(($) => $.composer.create_unreadable)}
          </AlertDescription>
        </Alert>
      ) : null}

      {/* An accepted generation always says something about itself. The detail
          read can be in flight, and it can fail or answer with a body the
          schema rejects — the poll stops on a failed read
          (../core/aurora/queries.ts) — and in all of those cases the drawer used
          to render nothing at all, which reads as "the submit did nothing" and
          invites a second one.

          A read the client could not use is reported next to the last status
          rather than instead of it: React Query keeps the data a failed refetch
          could not replace, so one dropped poll tick says "here is where it was,
          and I cannot see it now" instead of throwing away a status that was
          read successfully a moment ago. A degraded body leaves nothing to keep
          — it replaced the cache with a null — so it says only the first half. */}
      {outcome.kind === "started" ? (
        <>
          {generation ? (
            <div className="flex flex-col gap-2">
              <div className="flex items-center gap-2">
                <span
                  role="status"
                  className={GENERATION_STATUS_PILL[generationStatusLabel(generation.status)]}
                >
                  {t(
                    ($) =>
                      $.composer.status[generationStatusLabel(generation.status)],
                  )}
                </span>
                {!isTerminal ? <Spinner aria-hidden="true" /> : null}
              </div>
              {generationStatusLabel(generation.status) === "failed" ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.composer.failed_description)}
                </p>
              ) : null}
              {!isTerminal ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.composer.progress_description)}
                </p>
              ) : null}
            </div>
          ) : progressUnreadable ? null : (
            <div className="flex items-center gap-2">
              <Spinner aria-hidden="true" />
              <p className="text-caption text-muted-foreground">
                {t(($) => $.composer.progress_description)}
              </p>
            </div>
          )}

          {progressUnreadable ? (
            <Alert variant="destructive">
              <CircleAlert aria-hidden="true" />
              <AlertDescription>
                {t(($) => $.composer.progress_unreadable)}
              </AlertDescription>
              <AlertAction>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => void detail.refetch()}
                >
                  {t(($) => $.retry)}
                </Button>
              </AlertAction>
            </Alert>
          ) : null}
        </>
      ) : null}

      {showResult ? (
        <div className="aurora-drawer-section">
          <h3 className="text-label font-medium">
            {t(($) => $.composer.result_title)}
          </h3>
          {assets.length > 0 ? (
            <ul className="flex flex-col gap-1">
              {assets.map((asset) => (
                <li key={asset.id}>
                  <button
                    type="button"
                    onClick={() => void download(asset)}
                    aria-label={`${asset.format ?? asset.kind} ${t(($) => $.works.download)}`}
                    className="aurora-file-row"
                  >
                    <FileDown aria-hidden="true" className="size-3.5 shrink-0" />
                    <span>{asset.format ?? asset.kind}</span>
                    <span>{t(($) => $.works.download)}</span>
                  </button>
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.composer.result_empty)}
            </p>
          )}
          {worksHref ? (
            <AppLink
              href={worksHref}
              className="aurora-file-row"
            >
              <Sparkles aria-hidden="true" className="size-3.5" />
              {t(($) => $.composer.open_library)}
            </AppLink>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/**
 * Why the create call was refused, in the terms the user can act on.
 *
 * The server answers 402 for a wallet that cannot cover the skill and 429 for
 * all three of its gates — the monthly quota, the concurrency cap and the
 * frequency limit. The three 429s differ only in a server message that is not a
 * contract, so they share one line rather than guessing which fired; the 402 is
 * the one worth separating, because it names a number the user can change and
 * the one the drawer can point at the way to change it.
 */
function SubmitFailure({
  error,
  skill,
  topUpHref,
}: {
  error: unknown;
  skill: AuroraSkill;
  topUpHref?: string;
}) {
  const { t } = useT("aurora");
  const locale = useLocale();
  const { data: balancePayload } = useAuroraBalance();
  // A degraded balance parses to 0 rather than to nothing, which would turn the
  // fallback into a quoted figure. Read as absent, exactly like a pending or
  // failed wallet read.
  const balance = isAuroraDegraded(balancePayload)
    ? undefined
    : balancePayload?.value;

  if (isAuroraInsufficientCreditsError(error)) {
    const cost = formatCredits(skill.credits, locale);
    return (
      <Alert variant="destructive">
        <CircleAlert aria-hidden="true" />
        <AlertTitle>{t(($) => $.composer.insufficient_title)}</AlertTitle>
        <AlertDescription>
          {/* The 402 itself is what makes this prompt true, so the price alone
              is a complete sentence. The balance is added only when there is
              one to show: while the wallet query is pending, failed, or
              unreadable there is no number here, and "you have 0" would be a
              claim the client never read — the user would top up against a
              figure we invented. */}
          {balance === undefined
            ? t(($) => $.composer.insufficient_cost_only, { cost })
            : t(($) => $.composer.insufficient_description, {
                cost,
                balance: formatMicroCredits(balance.availableMicro, locale),
              })}
        </AlertDescription>
        {/* The one refusal the user can do something about, so it carries the
            way to do it — when the app has a checkout route to offer. */}
        {topUpHref ? (
          <AlertAction>
            <AppLink
              href={topUpHref}
              className="inline-flex items-center gap-1 text-body underline decoration-muted-foreground/30 underline-offset-4 transition-colors hover:text-foreground"
            >
              <CreditCard aria-hidden="true" className="size-3.5" />
              {t(($) => $.billing.top_up)}
            </AppLink>
          </AlertAction>
        ) : null}
      </Alert>
    );
  }

  if (isAuroraRateLimitError(error)) {
    return (
      <Alert variant="destructive">
        <CircleAlert aria-hidden="true" />
        <AlertTitle>{t(($) => $.composer.rate_limited_title)}</AlertTitle>
        <AlertDescription>
          {t(($) => $.composer.rate_limited_description)}
        </AlertDescription>
      </Alert>
    );
  }

  return (
    <Alert variant="destructive">
      <CircleAlert aria-hidden="true" />
      <AlertDescription>
        {t(($) => $.composer.create_failed)}
      </AlertDescription>
    </Alert>
  );
}
