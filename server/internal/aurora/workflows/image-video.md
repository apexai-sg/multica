# Image to Video

Animate one supplied image from a written request with the Volcengine Ark
Seedance model.

You run on a normal Multica agent: you have a shell and the network access this
node provides. Do the work yourself.

## Inputs

- `prompt`: the user's request (required). It is the generation prompt in your
  task message; use it verbatim. It must be at most 3000 characters.
- Exactly one attached image. Fetch it with `multica attachment download <id>`,
  which writes it locally and prints the path. It must be PNG or JPEG and at most
  25 MB.
- `ARK_PLATFORM_API_KEY` is the Ark platform key and `ARK_PLATFORM_BASE_URL` names
  the Ark endpoint; the video endpoints are built from that base.

## Steps

1. Open the create lease before spending a provider call:

   ```bash
   curl -fsS -X POST "$MULTICA_SERVER_URL/api/agent/tasks/$MULTICA_TASK_ID/aurora-provider-runs/begin" \
     -H "Authorization: Bearer $MULTICA_TOKEN" -H 'content-type: application/json' \
     -d '{"provider":"volcengine-ark","operation":"seedance.create","model":"doubao-seedance-2-5-260628","arguments":{}}'
   ```

   Stop and report a failure when the response has `create_allowed` false and no
   `external_id`. The lease is bookkeeping, not a hard guard: nothing at the
   provider stops a second call, so treating a refusal as fatal is what keeps
   this run to one create.

2. Create the task. The image goes in as a **Base64 data URI** — not a URL.
   Set `PROMPT` to the user's request verbatim and `IMAGE` to the downloaded
   file:

   ```bash
   case "${IMAGE##*.}" in
     jpg|jpeg) mime=image/jpeg ;;
     *)        mime=image/png ;;
   esac
   # One argument is capped at 128 KiB, so the data URI goes through a file
   # rather than through jq's argv.
   { printf 'data:%s;base64,' "$mime"; base64 -w0 "$IMAGE" | tr -d '\n'; } > /tmp/image-data-uri.txt
   jq -n --arg p "$PROMPT" --rawfile url /tmp/image-data-uri.txt '{
     model: "doubao-seedance-2-5-260628",
     content: [ { type: "text", text: $p }, { type: "image_url", image_url: { url: $url } } ]
   }' > /tmp/seedance-create-request.json

   curl -fsS -X POST "${ARK_PLATFORM_BASE_URL:-https://ark.cn-beijing.volces.com/api}/v3/contents/generations/tasks" \
     -H "Authorization: Bearer $ARK_PLATFORM_API_KEY" -H 'content-type: application/json' \
     --data-binary @/tmp/seedance-create-request.json -o /tmp/seedance-create.json
   ```

   The task id is the response's top-level `id`, and it looks like
   `cgt-<digits>-<suffix>`. Stop and report a failure when it is missing.

3. Poll the task every 5 seconds until it reaches a terminal state. The status
   is the response's top-level `status` field:

   ```bash
   task_id=$(jq -r '.id' /tmp/seedance-create.json)
   deadline=$(( $(date +%s) + 1800 ))   # 30 minutes, the supported ceiling
   while :; do
     curl -fsS "${ARK_PLATFORM_BASE_URL:-https://ark.cn-beijing.volces.com/api}/v3/contents/generations/tasks/$task_id" \
       -H "Authorization: Bearer $ARK_PLATFORM_API_KEY" -o /tmp/seedance-poll.json
     status=$(jq -r '.status // empty' /tmp/seedance-poll.json)
     case "$status" in
       succeeded) break ;;
       failed|expired|cancelled) echo "Seedance task ended as $status" >&2; exit 1 ;;
     esac
     [ "$(date +%s)" -ge "$deadline" ] && { echo "Seedance task timed out" >&2; exit 1; }
     sleep 5
   done
   ```

   The comparison is exact and lowercase. Any other status means the task is
   still running; keep polling until the deadline.

4. Hand the result URL to the task-scoped importer. Do not download it yourself:
   this workflow requires a server-staged artifact associated with the current
   task. Use the returned staging ID and integrity metadata in the manifest
   below, not the provider URL or a local download.

   ```bash
   result_url=$(jq -r '.content.video_url // empty' /tmp/seedance-poll.json)
   [ -n "$result_url" ] || { echo "Seedance succeeded without output" >&2; exit 1; }

   curl -fsS -X POST "$MULTICA_SERVER_URL/api/agent/tasks/$MULTICA_TASK_ID/aurora-artifacts/import" \
     -H "Authorization: Bearer $MULTICA_TOKEN" -H 'content-type: application/json' \
     -d "$(jq -n --arg u "$result_url" '{url:$u,kind:"video",name:"image-video.mp4",mime_type:"video/mp4",size_bytes:null,metadata:{}}')" \
     -o /tmp/import.json
   ```

   The response carries `staging_id`, `size_bytes` and `sha256`.

5. Close the run:

   ```bash
   curl -fsS -X PUT "$MULTICA_SERVER_URL/api/agent/tasks/$MULTICA_TASK_ID/aurora-provider-runs/seedance.create/finish" \
     -H "Authorization: Bearer $MULTICA_TOKEN" -H 'content-type: application/json' \
     -d '{"state":"succeeded"}'
   ```

## Required outputs

- One primary `video` artifact, staged on the server under the `staging_id` from
  step 4.

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
  "skill_id": "image-video",
  "producer": { "id": "byted-ark-seedance-skill", "version": "5.0.0", "tree_sha256": null },
  "provider_run": { "provider": "volcengine-ark", "model": "doubao-seedance-2-5-260628", "external_id": "cgt-..." },
  "artifacts": [
    {
      "id": "video-1",
      "source": { "type": "staged_object", "staging_id": "<staging_id from step 4>" },
      "name": "image-video.mp4",
      "kind": "video",
      "role": "primary",
      "format": "mp4",
      "mime_type": "video/mp4",
      "size_bytes": 0,
      "sha256": "<sha256 from step 4>",
      "metadata": {}
    }
  ]
}
```

Set `provider_run.external_id` to the `cgt-…` task id from step 2. The daemon
re-checks this manifest against the task and the skill: the producer id must be
`byted-ark-seedance-skill`, the skill id must be `image-video`, at least one
artifact must be `primary`, and `size_bytes` must be a non-negative integer. A
missing manifest, more than 20 artifacts, or more than 600 MiB in total is
rejected. Take `size_bytes` and `sha256` from the import response rather than the
placeholders above.

## Failure behavior

- Missing a required input: ask on the issue this run was claimed from —
  `multica issue comment add <issue id> --content-file ./question.md` — then end
  this round. Declare it in the manifest: `"outcome": "needs_input"` with
  `"artifacts": []`. Write no files and call no provider: anything you store is
  counted as an artifact and billed.
- A non-2xx from Seedance: report the status and the body, then call
  `.../seedance.create/finish` with `{"state":"failed","error_code":"provider_failed"}`,
  and write no manifest.
- A task that ends `failed`, `expired` or `cancelled`, or that is still running
  after 30 minutes: report that status. Do not start a second task.
- **Never submit a second Seedance create for the same task**, on any path: the
  create is billable and the provider cannot deduplicate it.
- Never claim completion unless step 4 returned a `staging_id` and the manifest
  names it.
