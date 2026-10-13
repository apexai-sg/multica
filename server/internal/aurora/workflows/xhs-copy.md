# Xiaohongshu Copy

Write Xiaohongshu (小红书) post copy from a written request and an optional
source document.

You run on a normal Multica agent: you have a shell. Do the work yourself —
the copy is yours to write.

## Inputs

- `prompt`: the user's instruction (required) — the topic, the angle, the tone,
  and any constraints. It may also carry the source material directly.
- Zero or one attached document (`.txt`, `.md`, `.markdown`, `.pdf` or
  `.docx`). Fetch it with `multica attachment download <id>`, which writes it
  locally and prints the path. It must be at most 25 MiB.
- No provider credential is needed.

## Steps

1. When a document is attached, read it into text. `DOC` is the downloaded file:

   ```bash
   case "${DOC##*.}" in
     txt|md|markdown) sed 's/^\xEF\xBB\xBF//' "$DOC" > /tmp/source.txt ;;
     pdf)  pdftotext "$DOC" - > /tmp/source.txt ;;
     docx) unzip -p "$DOC" word/document.xml \
             | sed -e 's/<w:tab[^>]*>/\t/g' -e 's/<w:br[^>]*>/\n/g' -e 's|</w:p>|\n|g' -e 's/<[^>]*>//g' \
             | sed -e 's/&lt;/</g' -e 's/&gt;/>/g' -e 's/&quot;/"/g' -e "s/&apos;/'/g" -e 's/&amp;/\&/g' \
             > /tmp/source.txt ;;
     *) echo "unsupported document format" >&2; exit 1 ;;
   esac
   ```

   Stop and report it when the document is unreadable or its format is not one
   of those.

2. Write the copy yourself, in Chinese unless the user asked otherwise, from the
   `prompt` and any document text. Ground every claim in what the user supplied:
   never invent a product fact, a price, an ingredient or an experience. Keep
   the angle and the length the user asked for.

   A Xiaohongshu post is a title line plus a body. Write what the user asked for
   and nothing the platform would reject — no fabricated statistics, no medical
   or efficacy claims, and no contact details that route readers off-platform
   unless the user asked for them.

3. Write the copy as the run's artifact:

   ```bash
   mkdir -p "<outputRoot>/artifacts" "<outputRoot>/.multica"
   # Write the copy to <outputRoot>/artifacts/xhs-copy.md
   ```

## Required outputs

- `<outputRoot>/artifacts/xhs-copy.md` — the copy, UTF-8 Markdown, at most
  2 MiB. It is the run's only artifact, and it is the primary one.

## Artifact manifest

Write `<outputRoot>/.multica/aurora-artifacts.v1.json`. Run
`mkdir -p "<outputRoot>/.multica"` first: nothing creates that directory for
you. A local file artifact
names its path **relative to the output root**; compute its size and hash from
the file you wrote.

```json
{
  "schema": "com.multica.aurora.artifacts",
  "version": 1,
  "task_id": "<the task id from your task context>",
  "skill_id": "xhs-copy",
  "producer": { "id": "multica-aurora-runtime", "version": "1.0.0", "tree_sha256": null },
  "provider_run": null,
  "artifacts": [
    {
      "id": "primary-1",
      "source": { "type": "file", "relative_path": "artifacts/xhs-copy.md" },
      "name": "xhs-copy.md",
      "kind": "text",
      "role": "primary",
      "format": "md",
      "mime_type": "text/markdown",
      "size_bytes": 0,
      "sha256": "sha256:<64 hex>",
      "metadata": {}
    }
  ]
}
```

The daemon re-checks this manifest against the task and the skill: the producer
id must be `multica-aurora-runtime`, the skill id must be `xhs-copy`, the
primary artifact must be a `text` output (this skill declares `text` only), the
file name's extension must match its kind, and `size_bytes` must be a
non-negative integer. A missing manifest, a symlink or a non-regular file, more
than 20 artifacts, or more than 600 MiB in total is rejected. Compute
`size_bytes` and `sha256` with `stat -c %s` and `sha256sum`.

Use only `.md`, `.markdown` or `.txt` for the artifact name: no other extension
is accepted for a text artifact.

## Failure behavior

- Missing a required input: ask on the issue this run was claimed from —
  `multica issue comment add <issue id> --content-file ./question.md` — then end
  this round. Declare it in the manifest: `"outcome": "needs_input"` with
  `"artifacts": []`. Write no files and call no provider: anything you store is
  counted as an artifact and billed.
- The request carries no topic or angle to write from: stop and report it. Do
  not guess a subject.
- The document cannot be downloaded or yields no text: report it. Do not write
  the copy from the file name.
- The user asked for a fact you do not have: leave it out rather than inventing
  it, and say so in your report.
- Never claim completion unless the copy file exists and the manifest names it.
