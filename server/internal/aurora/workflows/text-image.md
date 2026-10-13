# Text to Image

Turn a written request into one image with the Volcengine Ark Seedream model.

You run on a normal Multica agent: you have a shell and the network access this
node provides. Do the work yourself.

## Inputs

- `prompt`: the user's request (required). It is the generation prompt in your
  task message; use it verbatim.
- This skill takes no attachments. Use text only.
- `ANTHROPIC_API_KEY` is the Ark Agent Plan key and `ANTHROPIC_BASE_URL`
  names the Ark endpoint; the image endpoint is built from that base.

## Steps

1. Open the create lease before spending a provider call:

   ```bash
   curl -fsS -X POST "$MULTICA_SERVER_URL/api/agent/tasks/$MULTICA_TASK_ID/aurora-provider-runs/begin" \
     -H "Authorization: Bearer $MULTICA_TOKEN" -H 'content-type: application/json' \
     -d '{"provider":"volcengine-agentplan","operation":"seedream.generate","model":"doubao-seedream-5.0-lite","arguments":{}}'
   ```

   Stop and report a failure when the response has `create_allowed` false and no
   `external_id`. The lease is bookkeeping, not a hard guard: nothing at the
   provider stops a second call, so treating a refusal as fatal is what keeps
   this run to one create, and submitting anyway would bill a second generation
   for the same task.

2. Ask Seedream for the image. Set `PROMPT` to the user's request exactly as it
   appears in your task message, then run:

   ```bash
   curl -fsS -X POST "${ANTHROPIC_BASE_URL:-https://ark.cn-beijing.volces.com/api/plan}/v3/images/generations" \
     -H "Authorization: Bearer $ANTHROPIC_API_KEY" -H 'content-type: application/json' \
     -d "$(jq -n --arg p "$PROMPT" '{model:"doubao-seedream-5.0-lite",prompt:$p,size:"2K",response_format:"url",output_format:"png",watermark:false}')" \
     -o /tmp/seedream.json
   ```

   The result URL is `data[0].url`. Stop and report a failure when the response
   has no image.

3. Hand that URL to the task-scoped importer. Do not download it yourself: this
   workflow requires a server-staged artifact associated with the current task.
   Use the returned staging ID and integrity metadata in the manifest below,
   not the provider URL or a local download.

   ```bash
   curl -fsS -X POST "$MULTICA_SERVER_URL/api/agent/tasks/$MULTICA_TASK_ID/aurora-artifacts/import" \
     -H "Authorization: Bearer $MULTICA_TOKEN" -H 'content-type: application/json' \
     -d "$(jq -n --arg u "$(jq -r '.data[0].url' /tmp/seedream.json)" '{url:$u,kind:"image",name:"text-image.png",mime_type:"image/png",size_bytes:null,metadata:{}}')" \
     -o /tmp/import.json
   ```

   The response carries `staging_id`, `size_bytes` and `sha256`.

4. Close the run:

   ```bash
   curl -fsS -X PUT "$MULTICA_SERVER_URL/api/agent/tasks/$MULTICA_TASK_ID/aurora-provider-runs/seedream.generate/finish" \
     -H "Authorization: Bearer $MULTICA_TOKEN" -H 'content-type: application/json' \
     -d '{"state":"succeeded"}'
   ```

## Required outputs

- One primary `image` artifact, staged on the server under the `staging_id` from
  step 3.

## Artifact manifest

Write `<outputRoot>/.multica/aurora-artifacts.v1.json`. Run
`mkdir -p "<outputRoot>/.multica"` first: nothing creates that directory for
you. Every value below comes
from a previous step; copy it, do not invent it.

```json
{
  "schema": "com.multica.aurora.artifacts",
  "version": 1,
  "task_id": "<the task id from your task context>",
  "skill_id": "text-image",
  "producer": { "id": "byted-ark-seedream-skill", "version": "4.0.0", "tree_sha256": null },
  "provider_run": { "provider": "volcengine-agentplan", "model": "doubao-seedream-5.0-lite", "external_id": null },
  "artifacts": [
    {
      "id": "image-1",
      "source": { "type": "staged_object", "staging_id": "<staging_id from step 3>" },
      "name": "text-image.png",
      "kind": "image",
      "role": "primary",
      "format": "png",
      "mime_type": "image/png",
      "size_bytes": 0,
      "sha256": "<sha256 from step 3>",
      "metadata": {}
    }
  ]
}
```

The daemon re-checks this manifest against the task and the skill: the producer
id must be `byted-ark-seedream-skill`, the skill id must be `text-image`, at
least one artifact must be `primary`, and `size_bytes` must be a non-negative
integer. A missing manifest, more than 20 artifacts, or more than 600 MiB in
total is rejected. Take `size_bytes` and
`sha256` from the import response rather than the placeholders above.

## Failure behavior

- Missing a required input: ask on the issue this run was claimed from —
  `multica issue comment add <issue id> --content-file ./question.md` — then end
  this round. Declare it in the manifest: `"outcome": "needs_input"` with
  `"artifacts": []`. Write no files and call no provider: anything you store is
  counted as an artifact and billed.
- A non-2xx from Seedream: report the status and the body, then call
  `.../seedream.generate/finish` with `{"state":"failed","error_code":"provider_failed"}`,
  and write no manifest.
- Never submit a second Seedream create for the same task.
- Never claim completion unless step 3 returned a `staging_id` and the manifest
  names it.
