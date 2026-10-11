"use client";

import { useState } from "react";
import { ExternalLink, FileDown } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { buttonVariants } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  isAuroraGenerationTerminal,
  type AuroraAsset,
  type AuroraGeneration,
} from "@multica/core/aurora";
import { useT } from "../i18n";
import {
  generationStatusLabel,
  type AuroraGenerationStatusLabel,
} from "./labels";
import { useAuroraAssetDownload } from "./use-aurora-asset-download";

/**
 * The shared rendering of a generation's status and its produced files.
 *
 * The history list and the generation detail view answer the same two
 * questions about a generation — how did it end, and what came back — so the
 * pieces live here rather than as two copies free to disagree about terminal
 * handling, the thumbnail cap, or the download route.
 *
 * A generation can carry many files. Image artifacts render as thumbnails up
 * to a bounded count, and every other artifact — and any image past the cap —
 * is a labelled download row. The cap keeps first paint from fetching an
 * unbounded number of images while leaving the overflow reachable.
 */

/** How many thumbnails one generation loads before the rest become file rows. */
export const MAX_GENERATION_THUMBNAILS = 4;

/** The asset and the generation it belongs to, held while the dialog is open. */
interface PreviewState {
  asset: AuroraAsset;
  generation: AuroraGeneration;
}

/**
 * Whether an asset has an image to render, narrowing away an unusable URL.
 *
 * A missing URL is not the only unusable one: the server can emit an empty
 * `mediaUrl` for a valid-empty pointer, and a relative or non-http(s) value is
 * not an image the browser can load. Each of those takes the download-row path
 * rather than rendering a broken thumbnail.
 */
export function isPreviewableImage(
  asset: AuroraAsset,
): asset is AuroraAsset & { mediaUrl: string } {
  return asset.kind === "image" && isAbsoluteHttpUrl(asset.mediaUrl);
}

/** A non-empty absolute http(s) URL, the only form an `<img>` can load. */
function isAbsoluteHttpUrl(url: string | null): url is string {
  if (!url) return false;
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return false;
  }
  return (
    (parsed.protocol === "http:" || parsed.protocol === "https:") &&
    parsed.host !== ""
  );
}

/**
 * What a generation charges, or null when there is nothing to say.
 *
 * A failed generation is refunded in full, so the amount it reserved is not a
 * cost. A settled generation reports what settlement actually charged — which
 * can be zero, and zero is not a spend. Only a generation still in flight shows
 * its reservation, because that is the only amount that currently exists.
 */
export function generationCredits(
  generation: AuroraGeneration,
): number | null {
  if (generationStatusLabel(generation.status) === "failed") return null;
  if (isAuroraGenerationTerminal(generation.status)) {
    return generation.creditsCharged > 0 ? generation.creditsCharged : null;
  }
  return generation.creditsReserved > 0 ? generation.creditsReserved : null;
}

/** Shared pill vocabulary; unrecognised statuses stay neutral. */
export const GENERATION_STATUS_PILL: Record<AuroraGenerationStatusLabel, string> = {
  queued: "aurora-status-pill aurora-status-pill--working",
  running: "aurora-status-pill aurora-status-pill--working",
  completed: "aurora-status-pill",
  failed: "aurora-status-pill aurora-status-pill--failed",
  unknown: "aurora-status-pill aurora-status-pill--muted",
};

export function GenerationStatusBadge({
  status,
  className,
}: {
  status: string;
  className?: string;
}) {
  const { t } = useT("aurora");
  const label = generationStatusLabel(status);
  return (
    <span data-slot="badge" className={cn(GENERATION_STATUS_PILL[label], className)}>
      {t(($) => $.composer.status[label])}
    </span>
  );
}

/**
 * A generation's artifacts: bounded image thumbnails, non-image download rows,
 * and the larger view a thumbnail opens.
 *
 * The whole block, including the dialog, is internal so both callers get the
 * same interaction. The empty copy is only shown once the generation is
 * terminal: "no files were produced" is a claim about a finished generation,
 * not about one still working.
 */
export function GenerationArtifacts({
  generation,
  assets,
  loading = false,
  maxThumbnails = MAX_GENERATION_THUMBNAILS,
}: {
  generation: AuroraGeneration;
  assets: AuroraAsset[];
  /** Show the loading placeholder instead of the files. */
  loading?: boolean;
  maxThumbnails?: number;
}) {
  const { t } = useT("aurora");
  const download = useAuroraAssetDownload();
  const [preview, setPreview] = useState<PreviewState | null>(null);

  if (loading) {
    // Reserve the geometry the loaded block occupies: a line of thumbnails at
    // the cap below, plus the gap-2 wrapper the files use. Without it the row
    // grows from one thumbnail to the real files as soon as the read lands.
    return (
      <div className="flex flex-col gap-2" aria-hidden="true">
        <div className="aurora-thumbs">
          {Array.from({ length: maxThumbnails }, (_, index) => (
            <Skeleton key={index} className="size-20 rounded-lg" />
          ))}
        </div>
      </div>
    );
  }

  const images = assets.filter(isPreviewableImage);
  const previewable = images.slice(0, maxThumbnails);
  const files = [
    ...assets.filter((asset) => !isPreviewableImage(asset)),
    ...images.slice(maxThumbnails),
  ];

  if (previewable.length === 0 && files.length === 0) {
    return isAuroraGenerationTerminal(generation.status) ? (
      <p className="text-caption text-muted-foreground">
        {t(($) => $.composer.result_empty)}
      </p>
    ) : null;
  }

  return (
    // min-h keeps a download-only result from shrinking the row below the
    // thumbnail line the loading placeholder reserves.
    <div className="flex min-h-20 flex-col gap-2">
      {previewable.length > 0 ? (
        <ul className="aurora-thumbs">
          {previewable.map((asset) => (
            <li key={asset.id}>
              <button
                type="button"
                onClick={() => setPreview({ asset, generation })}
                aria-label={t(($) => $.history.preview_label, {
                  format: asset.format ?? asset.kind,
                })}
                className="aurora-thumb"
              >
                {/* A fixed box with width and height keeps a slow-loading
                    image from moving the rows below it. */}
                <img
                  src={asset.mediaUrl}
                  alt=""
                  loading="lazy"
                  width={80}
                  height={80}
                  className="size-full object-cover"
                />
              </button>
            </li>
          ))}
        </ul>
      ) : null}
      {files.length > 0 ? (
        <ul className="flex flex-col gap-1">
          {files.map((asset) => (
            <li key={asset.id}>
              {/* The two spans read as one word to an assistive name, so the
                  label spells out the separator the layout only draws. */}
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
      ) : null}
      <PreviewDialog
        preview={preview}
        onOpenChange={(open) => {
          if (!open) setPreview(null);
        }}
      />
    </div>
  );
}

/**
 * The larger view a thumbnail opens: the full-size file, a link to its own
 * media URL, and the download route.
 *
 * DialogTitle is required for the dialog's accessible name; the generation's
 * prompt is the most useful one, with the shared empty-prompt copy as fallback.
 */
function PreviewDialog({
  preview,
  onOpenChange,
}: {
  preview: PreviewState | null;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("aurora");
  const download = useAuroraAssetDownload();
  return (
    <Dialog open={preview !== null} onOpenChange={onOpenChange}>
      <DialogContent className="aurora-dialog aurora-dialog--wide">
        <DialogHeader>
          <DialogTitle>
            {preview?.generation.prompt || t(($) => $.works.no_prompt)}
          </DialogTitle>
          <DialogDescription>
            {t(($) => $.history.preview_description)}
          </DialogDescription>
        </DialogHeader>
        {preview?.asset.mediaUrl ? (
          <img
            src={preview.asset.mediaUrl}
            alt=""
            className="max-h-[70vh] w-full rounded-lg object-contain"
          />
        ) : null}
        <DialogFooter>
          {preview?.asset.mediaUrl ? (
            <a
              href={preview.asset.mediaUrl}
              target="_blank"
              rel="noopener noreferrer"
              className={buttonVariants({ variant: "outline", size: "sm" })}
            >
              <ExternalLink aria-hidden="true" className="size-3.5" />
              {t(($) => $.history.open_in_new_tab)}
            </a>
          ) : null}
          {preview ? (
            <button
              type="button"
              onClick={() => void download(preview.asset)}
              className={buttonVariants({ variant: "default", size: "sm" })}
            >
              <FileDown aria-hidden="true" className="size-3.5" />
              {t(($) => $.works.download)}
            </button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
