# Document Summary

Summarise one supplied document and write the summary out as text.

You run on a normal Multica agent: you have a shell and the Multica CLI. Do the
work yourself — there is no summarisation service behind this skill.

## Inputs

- `prompt`: the user's instruction (required). It is the summary request in your
  task message; use it verbatim.
- Exactly one attached document (`.txt`, `.md`, `.markdown`, `.pdf` or `.docx`).
  Fetch it with `multica attachment download <id>`, which writes it locally and
  prints the path. It must be at most 25 MiB.
- No provider credential is needed: the summary is yours to write.

## Steps

1. Read the document into text. `DOC` is the downloaded file:

   ```bash
   case "${DOC##*.}" in
     txt|md|markdown) sed 's/^\xEF\xBB\xBF//' "$DOC" > /tmp/document.txt ;;
     pdf)  pdftotext "$DOC" - > /tmp/document.txt ;;
     docx) unzip -p "$DOC" word/document.xml \
             | sed -e 's/<w:tab[^>]*>/\t/g' -e 's/<w:br[^>]*>/\n/g' -e 's|</w:p>|\n|g' -e 's/<[^>]*>//g' \
             | sed -e 's/&lt;/</g' -e 's/&gt;/>/g' -e 's/&quot;/"/g' -e "s/&apos;/'/g" -e 's/&amp;/\&/g' \
             > /tmp/document.txt ;;
     *) echo "unsupported document format" >&2; exit 1 ;;
   esac
   [ -s /tmp/document.txt ] || { echo "document produced no text" >&2; exit 1; }
   ```

   `pdftotext` writes to stdout when the second argument is `-`. The DOCX recipe
   unpacks `word/document.xml` and strips its markup; it is a shell equivalent of
   the reader this skill used to call, so a stray numeric entity (`&#38;`) may
   survive — read around it, do not present it as content.

2. Read the whole text and write the summary yourself, following the user's
   instruction: the language they asked in, the length they asked for, and the
   points they asked about. Never introduce a fact the document does not state.

3. Write the summary as the run's artifact:

   ```bash
   mkdir -p "<outputRoot>/artifacts" "<outputRoot>/.multica"
   # Write your summary to <outputRoot>/artifacts/document-summary.md
   ```

## Required outputs

- `<outputRoot>/artifacts/document-summary.md` — the summary, UTF-8 Markdown, at
  most 2 MiB. It is the run's only artifact, and it is the primary one.

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
  "skill_id": "document-summary",
  "producer": { "id": "multica-aurora-runtime", "version": "1.0.0", "tree_sha256": null },
  "provider_run": null,
  "artifacts": [
    {
      "id": "primary-1",
      "source": { "type": "file", "relative_path": "artifacts/document-summary.md" },
      "name": "document-summary.md",
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
id must be `multica-aurora-runtime`, the skill id must be `document-summary`, the
primary artifact must be a `text` output, the file name's extension must match
its kind, and `size_bytes` must be a non-negative integer. A missing manifest, a
symlink or a non-regular file, more than 20 artifacts, or more than 600 MiB in
total is rejected. Compute `size_bytes` and `sha256` with `stat -c %s` and
`sha256sum`.

## Failure behavior

- Missing a required input: ask on the issue this run was claimed from —
  `multica issue comment add <issue id> --content-file ./question.md` — then end
  this round. Declare it in the manifest: `"outcome": "needs_input"` with
  `"artifacts": []`. Write no files and call no provider: anything you store is
  counted as an artifact and billed.
- The document cannot be downloaded, is not one of the supported formats, or
  yields no text: stop and report it. Do not summarise from the file name.
- The text is truncated by the extraction (a very large `pdf`): say so in the
  summary rather than implying you read all of it.
- Never claim completion unless the summary file exists and the manifest names
  it.
