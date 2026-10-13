# ID Photo

Turn one supplied portrait into an identification photo with ImageMagick.

You run on a normal Multica agent: you have a shell. Do the work yourself.

## Inputs

- `prompt`: the user's instruction (required).
- Exactly one attached image (`.png` or `.jpg`/`.jpeg`). Fetch it with
  `multica attachment download <id>`, which writes it locally and prints the
  path. It must be at most 25 MiB.
- No provider credential is needed: this skill runs entirely on this node.

## Steps

1. Produce the photo with one fixed transform. `INPUT` is the downloaded image:

   ```bash
   mkdir -p "<outputRoot>/artifacts" "<outputRoot>/.multica"
   convert -auto-orient "$INPUT" -colorspace sRGB -resize '600x600^' \
     -background white -gravity center -extent 600x600 \
     "<outputRoot>/artifacts/id-photo.png"
   ```

   The result is exactly 600×600 PNG: the image is scaled to fill that square and
   the overflow is cropped from the centre, with white filling any gap. Do not
   change the geometry, the background or the gravity, and do not crop to a face
   you chose yourself — the transform above is the whole skill.

2. Check the result exists and is a PNG:

   ```bash
   [ -s "<outputRoot>/artifacts/id-photo.png" ] || { echo "id-photo produced no file" >&2; exit 1; }
   ```

## Required outputs

- `<outputRoot>/artifacts/id-photo.png` — one image, at most 25 MiB. It is the
  run's only artifact, and it is the primary one.

## Artifact manifest

Write `<outputRoot>/.multica/aurora-artifacts.v1.json`. Run
`mkdir -p "<outputRoot>/.multica"` first: nothing creates that directory for
you. A local file artifact
names its path **relative to the output root**; the daemon re-derives the size
and hash from the file, so copy what you measured.

```json
{
  "schema": "com.multica.aurora.artifacts",
  "version": 1,
  "task_id": "<the task id from your task context>",
  "skill_id": "id-photo",
  "producer": { "id": "multica-aurora-runtime", "version": "1.0.0", "tree_sha256": null },
  "provider_run": null,
  "artifacts": [
    {
      "id": "primary-1",
      "source": { "type": "file", "relative_path": "artifacts/id-photo.png" },
      "name": "id-photo.png",
      "kind": "image",
      "role": "primary",
      "format": "png",
      "mime_type": "image/png",
      "size_bytes": 0,
      "sha256": "sha256:<64 hex>",
      "metadata": {}
    }
  ]
}
```

The daemon re-checks this manifest against the task and the skill: the producer
id must be `multica-aurora-runtime`, the skill id must be `id-photo`, the primary
artifact must be an `image` output, the file name's extension must match its
kind, and `size_bytes` must be a non-negative integer. A missing manifest, a
symlink or a non-regular file, more than 20 artifacts, or more than 600 MiB in
total is rejected. Compute `size_bytes` and `sha256` from the file
(`stat -c %s` and `sha256sum`).

## Failure behavior

- Missing a required input: ask on the issue this run was claimed from —
  `multica issue comment add <issue id> --content-file ./question.md` — then end
  this round. Declare it in the manifest: `"outcome": "needs_input"` with
  `"artifacts": []`. Write no files and call no provider: anything you store is
  counted as an artifact and billed.
- The attachment is missing, unreadable, or not a PNG/JPEG: stop and report it.
  Do not synthesise a portrait.
- `convert` exits non-zero: report its stderr and stop. Do not retry with a
  different geometry.
- Never claim completion unless the output file exists and the manifest names it.
