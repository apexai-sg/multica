# Resume

Build a resume from a written request and an optional source document.

You run on a normal Multica agent: you have a shell and Chromium. Do the work
yourself — there is no rendering service behind this skill.

## Inputs

- `prompt`: the user's instruction (required). The resume's content comes from
  it, and from the source document when one is attached.
- Zero or one attached document (`.txt`, `.md`, `.markdown`, `.pdf` or `.docx`).
  Fetch it with `multica attachment download <id>`, which writes it locally and
  prints the path. It must be at most 25 MiB.
- No provider credential is needed: this skill runs entirely on this node.

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
   of those. Never invent resume content the user did not supply.

2. Decide the sections from the `prompt` (and the source document). The
   sections, in render order, are:

   - `name` — required; the resume is rejected without it.
   - `title` — optional, one line under the name.
   - `contact` — optional list, joined with ` · `.
   - `summary` — optional paragraph.
   - `experience` — optional list of `role` / `company` / `dates` / `bullets`.
   - `education` — optional list of `degree` / `school` / `dates`.
   - `skills` — optional list, joined with `, `.

   Omit a section rather than filling it with a placeholder.

3. Write an HTML document with those sections. Escape every value you
   interpolate — `&` first, then `<`, `>`, `"`, `'` — so that user text cannot
   become markup. Keep this head and this stylesheet verbatim; they are the
   skill's rendering, not a starting point:

   ```html
   <!doctype html>
   <html lang="en">
   <head>
   <meta charset="utf-8">
   <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'">
   <style>body{font-family:system-ui,sans-serif;margin:48px;color:#111}h1{margin:0}h3{margin:0}.dates{color:#555;margin:2px 0 8px}.item{margin-bottom:16px}</style>
   </head>
   <body>
   <h1>NAME</h1>
   ...
   </body>
   </html>
   ```

   Headings are `<h1>` for the name, `<h3>` for a role or degree, and `<p
   class="dates">` for its dates; wrap each experience or education entry in
   `<div class="item">`. The CSP above is what keeps the page offline — do not
   relax it, and do not add scripts, images or fonts.

   ```bash
   mkdir -p "<outputRoot>/artifacts" "<outputRoot>/.multica"
   # Write the HTML to <outputRoot>/.multica/resume.html
   ```

4. Render the PDF with headless Chromium. Run it from the output root and name
   both paths absolutely; `--print-to-pdf` and `file://` each take exactly one
   value with no space after the `=`:

   ```bash
   cd "<outputRoot>"
   chromium --headless=new --no-sandbox --disable-gpu \
     --disable-background-networking --disable-features=NetworkService \
     "--host-resolver-rules=MAP * ~NOTFOUND" \
     --no-pdf-header-footer \
     "--print-to-pdf=$PWD/artifacts/resume.pdf" \
     "file://$PWD/.multica/resume.html"
   [ -s "<outputRoot>/artifacts/resume.pdf" ] || { echo "chromium produced no PDF" >&2; exit 1; }
   ```

   Quote `--host-resolver-rules=MAP * ~NOTFOUND` as one argument: it contains
   spaces. `--no-sandbox` is required because the node runs as an unprivileged
   user; keep it. Do not add `--no-network`-style flags beyond these — the
   resolver rule is what makes the render offline.

   Use a plain stem for the artifact name (default `resume`). A name like
   `resume.v2` loses its last `.v2` segment.

5. Write the same resume as Markdown, as the run's supporting artifact. It
   carries no HTML escaping: `# <name>`, then `## Experience`, `## Education`
   and `## Skills` for the sections you included.

   ```bash
   # Write it to <outputRoot>/artifacts/resume.md
   ```

## Required outputs

- `<outputRoot>/artifacts/resume.pdf` — the rendered resume. It is the run's
  **primary** artifact.
- `<outputRoot>/artifacts/resume.md` — the same resume as Markdown, a
  **supporting** artifact.

## Artifact manifest

Write `<outputRoot>/.multica/aurora-artifacts.v1.json`. Local file artifacts
name their path **relative to the output root**; compute each size and hash from
the file you wrote. The intermediate HTML is a temporary file and is not listed.

```json
{
  "schema": "com.multica.aurora.artifacts",
  "version": 1,
  "task_id": "<the task id from your task context>",
  "skill_id": "resume",
  "producer": { "id": "multica-aurora-runtime", "version": "1.0.0", "tree_sha256": null },
  "provider_run": null,
  "artifacts": [
    {
      "id": "resume-pdf",
      "source": { "type": "file", "relative_path": "artifacts/resume.pdf" },
      "name": "resume.pdf",
      "kind": "pdf",
      "role": "primary",
      "format": "pdf",
      "mime_type": "application/pdf",
      "size_bytes": 0,
      "sha256": "sha256:<64 hex>",
      "metadata": {}
    },
    {
      "id": "resume-markdown",
      "source": { "type": "file", "relative_path": "artifacts/resume.md" },
      "name": "resume.md",
      "kind": "text",
      "role": "supporting",
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
id must be `multica-aurora-runtime`, the skill id must be `resume`, the primary
artifact must be a `pdf` output (this skill declares `pdf` and `text`), every
file name's extension must match its kind, and `size_bytes` must be a
non-negative integer. A missing manifest, a symlink or a non-regular file, more
than 20 artifacts, or more than 600 MiB in total is rejected. Compute
`size_bytes` and `sha256` with `stat -c %s` and `sha256sum`.

## Failure behavior

- Missing a required input: ask on the issue this run was claimed from —
  `multica issue comment add <issue id> --content-file ./question.md` — then end
  this round. Declare it in the manifest: `"outcome": "needs_input"` with
  `"artifacts": []`. Write no files and call no provider: anything you store is
  counted as an artifact and billed.
- No `name` in the request, or a document that cannot be read: stop and report
  it. Do not fall back to a placeholder name.
- `chromium` exits non-zero or writes no PDF: report its stderr and stop. Do not
  publish the Markdown alone as a substituted result — the run's primary output
  is the PDF.
- A value that came from the user or the document is always escaped before it
  reaches the HTML. A resume that renders user text as markup is a failure, not
  a formatting quirk.
- Never claim completion unless both files exist and the manifest names them.
