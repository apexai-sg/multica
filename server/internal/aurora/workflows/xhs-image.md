# Xiaohongshu Image

Turn a written request into one social image with the Volcengine
Ark Seedream model, using any supplied images as references.

You run on a normal Multica agent: you have a shell and the network access this
node provides. Do the work yourself.

## Inputs

- `prompt`: the user's request (required). It is the generation prompt in your
  task message; use it verbatim.
- Zero to four attached reference images. Fetch each
  with `multica attachment download <id>`, which writes it locally and prints
  the path.
- `ARK_PLATFORM_API_KEY` is the Ark platform key and `ARK_PLATFORM_BASE_URL` names
  the Ark endpoint; the image endpoint is built from that base.

## Steps

1. Open the create lease before spending a provider call:

   ```bash
   curl -fsS -X POST "$MULTICA_SERVER_URL/api/agent/tasks/$MULTICA_TASK_ID/aurora-provider-runs/begin" \
     -H "Authorization: Bearer $MULTICA_TOKEN" -H 'content-type: application/json' \
     -d '{"provider":"volcengine-ark","operation":"seedream.generate","model":"doubao-seedream-5-0-pro-260628","arguments":{}}'
   ```

   Stop and report a failure when the response has `create_allowed` false and no
   `external_id`. The lease is bookkeeping, not a hard guard: nothing at the
   provider stops a second call, so treating a refusal as fatal is what keeps
   this run to one create.

2. Build the reference list and ask Seedream for the social image. Set `PROMPT` to the
   user's request exactly as it appears in your task message, and put the paths
   `multica attachment download` printed into `REFS` (an empty array is correct
   when the run has no attachments):

   ```bash
   REFS=()   # e.g. REFS=(/path/one.png /path/two.jpg)
   jq -n --arg p "$PROMPT" '{
     model: "doubao-seedream-5-0-pro-260628", prompt: $p,
     size: "2K", response_format: "url", output_format: "png", watermark: false
   }' > /tmp/seedream-request.json

   if [ ${#REFS[@]} -gt 0 ]; then
     # Assemble the body in files, never in argv: one argument is capped at
     # 128 KiB and a single image's base64 is already larger. Seedream takes
     # reference images in `image`, as one data URI or a list of them.
     rm -f /tmp/seedream-refs.jsonl
     for f in "${REFS[@]}"; do
       case "${f##*.}" in
         jpg|jpeg) mime=image/jpeg ;;
         *)        mime=image/png ;;
       esac
       printf 'data:%s;base64,' "$mime" >> /tmp/seedream-refs.jsonl
       base64 -w0 "$f" | tr -d '\n' >> /tmp/seedream-refs.jsonl
       printf '\n' >> /tmp/seedream-refs.jsonl
     done
     jq -Rn '[inputs]' /tmp/seedream-refs.jsonl > /tmp/seedream-refs.json
     jq --slurpfile r /tmp/seedream-refs.json \
       '. + {image: ($r[0] | if length == 1 then .[0] else . end)}' \
       /tmp/seedream-request.json > /tmp/seedream-request.tmp &&
       mv /tmp/seedream-request.tmp /tmp/seedream-request.json
   fi

   curl -fsS -X POST "${ARK_PLATFORM_BASE_URL:-https://ark.cn-beijing.volces.com/api}/v3/images/generations" \
     -H "Authorization: Bearer $ARK_PLATFORM_API_KEY" -H 'content-type: application/json' \
     --data-binary @/tmp/seedream-request.json -o /tmp/seedream.json
   ```

   The result URLs are `data[].url`. Stop and report a failure when the response
   carries no image.

3. Hand every URL to the task-scoped importer, first as `primary` and the rest as
   `supporting`. Do not download them yourself: this workflow requires a
   server-staged artifact associated with the current task. Use the returned
   staging ID and integrity metadata in the manifest below, not the provider URL
   or a local download.

   ```bash
   jq -r '.data[].url' /tmp/seedream.json | nl -ba | while read -r n url; do
     name="xhs-image.png"; [ "$n" -gt 1 ] && name="xhs-image-$n.png"
     curl -fsS -X POST "$MULTICA_SERVER_URL/api/agent/tasks/$MULTICA_TASK_ID/aurora-artifacts/import" \
       -H "Authorization: Bearer $MULTICA_TOKEN" -H 'content-type: application/json' \
       -d "$(jq -n --arg u "$url" --arg n "$name" \
         '{url:$u,kind:"image",name:$n,mime_type:"image/png",size_bytes:null,metadata:{}}')" \
       | tee "/tmp/import-$n.json"
   done
   ```

   Each response carries `staging_id`, `size_bytes` and `sha256`. The first
   import is the run's primary artifact, so it must succeed.

4. Close the run:

   ```bash
   curl -fsS -X PUT "$MULTICA_SERVER_URL/api/agent/tasks/$MULTICA_TASK_ID/aurora-provider-runs/seedream.generate/finish" \
     -H "Authorization: Bearer $MULTICA_TOKEN" -H 'content-type: application/json' \
     -d '{"state":"succeeded"}'
   ```

## Required outputs

- One primary `image` artifact, staged on the server under the first import's
  `staging_id`, plus one `supporting` image artifact for each further URL the
  provider returned.

## Artifact manifest

Write `<outputRoot>/.multica/aurora-artifacts.v1.json`. Run
`mkdir -p "<outputRoot>/.multica"` first: nothing creates that directory for
you. Every value below comes
from a previous step; copy it, do not invent it. One entry per imported object.

```json
{
  "schema": "com.multica.aurora.artifacts",
  "version": 1,
  "task_id": "<the task id from your task context>",
  "skill_id": "xhs-image",
  "producer": { "id": "byted-ark-seedream-skill", "version": "4.0.0", "tree_sha256": null },
  "provider_run": { "provider": "volcengine-ark", "model": "doubao-seedream-5-0-pro-260628", "external_id": null },
  "artifacts": [
    {
      "id": "image-1",
      "source": { "type": "staged_object", "staging_id": "<staging_id from the first import>" },
      "name": "xhs-image.png",
      "kind": "image",
      "role": "primary",
      "format": "png",
      "mime_type": "image/png",
      "size_bytes": 0,
      "sha256": "<sha256 from that import>",
      "metadata": {}
    }
  ]
}
```

The daemon re-checks this manifest against the task and the skill: the producer
id must be `byted-ark-seedream-skill`, the skill id must be `xhs-image`, at least
one artifact must be `primary`, and `size_bytes` must be a non-negative integer.
A missing manifest, more than 20 artifacts, or more than 600 MiB in total is
rejected. Take `size_bytes` and `sha256` from each import response rather than
the placeholders above.

## Failure behavior

- Missing a required input: ask on the issue this run was claimed from —
  `multica issue comment add <issue id> --content-file ./question.md` — then end
  this round. Declare it in the manifest: `"outcome": "needs_input"` with
  `"artifacts": []`. Write no files and call no provider: anything you store is
  counted as an artifact and billed.
- A non-2xx from Seedream: report the status and the body, then call
  `.../seedream.generate/finish` with `{"state":"failed","error_code":"provider_failed"}`,
  and write no manifest.
- An attachment that cannot be downloaded or read: stop. Do not generate a
  social image from a reference you never saw.
- Never submit a second Seedream create for the same task.
- Never claim completion unless the first import returned a `staging_id` and the
  manifest names it.
