# Transcription

Turn one supplied audio or video recording into a transcript with the
Volcengine speech model.

You run on a normal Multica agent: you have a shell and the network access this
node provides. Do the work yourself.

## Inputs

- `prompt`: the user's request (required).
- Exactly one attached recording: audio (`.wav`, `.mp3`, `.ogg`, `.opus`) or
  video (`.mp4`, `.mov`, `.webm`). Fetch it with
  `multica attachment download <id>`, which writes it locally and prints the
  path. It must be at most 100 MiB.
- `VOLC_ASR_API_KEY` is the Volcengine speech key. It is a different product and
  a different key from `ANTHROPIC_API_KEY`; the speech endpoint rejects the Ark
  key.

## Steps

1. Open the create lease before spending a provider call:

   ```bash
   curl -fsS -X POST "$MULTICA_SERVER_URL/api/agent/tasks/$MULTICA_TASK_ID/aurora-provider-runs/begin" \
     -H "Authorization: Bearer $MULTICA_TOKEN" -H 'content-type: application/json' \
     -d '{"provider":"volcengine-asr","operation":"asr.recognize","model":"bigmodel","arguments":{}}'
   ```

   Stop and report a failure when the response has `create_allowed` false and no
   `external_id`.

2. Prepare the audio. For a **video** attachment, extract the audio first —
   `INPUT` is the downloaded file:

   ```bash
   ffprobe -v error -print_format json -show_format -show_streams "$INPUT" > /tmp/probe.json
   # Stop if the duration is missing or above 7200 seconds, if there is no audio
   # or no video stream, or if the video codec is not one of
   # h264, hevc, vp8, vp9, av1.
   ffmpeg -y -i "$INPUT" -vn -ac 1 -ar 16000 -f wav /tmp/asr.wav
   ```

   For an **audio** attachment, use the file unchanged: no resampling or
   re-encoding happens on that path. Set `AUDIO` to the extracted wav or to the
   downloaded audio file, and `FORMAT` to `wav` (or `mp3`/`ogg` for those two
   audio types; `.opus` is sent as `ogg`).

3. Send the recording in one request. The service answers synchronously — there
   is no task to poll and no callback:

   ```bash
   # One argument is capped at 128 KiB, so the recording reaches jq through a
   # file instead. Newlines are stripped so `--rawfile` sees the base64 alone.
   base64 -w0 "$AUDIO" | tr -d '\n' > /tmp/asr.b64
   jq -n --arg uid "$MULTICA_TASK_ID" --arg fmt "$FORMAT" --rawfile data /tmp/asr.b64 '{
     user: { uid: $uid },
     audio: { format: $fmt, data: $data },
     request: { model_name: "bigmodel", enable_punc: true, enable_itn: true }
   }' > /tmp/asr-request.json

   curl -fsS -D /tmp/asr-headers.txt -X POST "https://openspeech.bytedance.com/api/v3/auc/bigmodel/recognize/flash" \
     -H 'content-type: application/json' \
     -H "x-api-key: $VOLC_ASR_API_KEY" \
     -H 'x-api-resource-id: volc.bigasr.auc_turbo' \
     -H "x-api-request-id: $(cat /proc/sys/kernel/random/uuid)" \
     -H 'x-api-sequence: -1' \
     --data-binary @/tmp/asr-request.json \
     -o /tmp/asr.json
   ```

4. Check the outcome. Success is the **response header** `x-api-status-code`
   equal to `20000000`; the transcript is the body's `result.text`:

   ```bash
   grep -qi '^x-api-status-code: 20000000' /tmp/asr-headers.txt || {
     echo "Volcengine ASR failed: $(grep -i '^x-api-status-code:' /tmp/asr-headers.txt)" >&2; exit 1; }
   jq -r '.result.text // empty' /tmp/asr.json | grep -q . || {
     echo "Volcengine ASR returned no transcript text" >&2; exit 1; }
   ```

5. Write the transcript as the run's artifact:

   ```bash
   mkdir -p "<outputRoot>/artifacts" "<outputRoot>/.multica"
   jq -r '.result.text' /tmp/asr.json > "<outputRoot>/artifacts/transcription.txt"
   ```

6. Close the run:

   ```bash
   curl -fsS -X PUT "$MULTICA_SERVER_URL/api/agent/tasks/$MULTICA_TASK_ID/aurora-provider-runs/asr.recognize/finish" \
     -H "Authorization: Bearer $MULTICA_TOKEN" -H 'content-type: application/json' \
     -d '{"state":"succeeded"}'
   ```

## Required outputs

- `<outputRoot>/artifacts/transcription.txt` — the transcript, as plain text. It
  is the run's only artifact, and it is the primary one.

## Artifact manifest

Write `<outputRoot>/.multica/aurora-artifacts.v1.json`. Run
`mkdir -p "<outputRoot>/.multica"` first: nothing creates that directory for
you. A local file artifact
names its path **relative to the output root**; the daemon re-derives the size
and hash from the file itself, so copy what you measured rather than inventing
it.

```json
{
  "schema": "com.multica.aurora.artifacts",
  "version": 1,
  "task_id": "<the task id from your task context>",
  "skill_id": "transcription",
  "producer": { "id": "volcengine-asr", "version": "1.0.0", "tree_sha256": null },
  "provider_run": { "provider": "volcengine-asr", "model": "bigmodel", "external_id": null },
  "artifacts": [
    {
      "id": "transcript-1",
      "source": { "type": "file", "relative_path": "artifacts/transcription.txt" },
      "name": "transcription.txt",
      "kind": "text",
      "role": "primary",
      "format": "txt",
      "mime_type": "text/plain",
      "size_bytes": 0,
      "sha256": "sha256:<64 hex>",
      "metadata": {}
    }
  ]
}
```

The daemon re-checks this manifest against the task and the skill: the producer
id must be `volcengine-asr`, the skill id must be `transcription`, the primary
artifact must be a `text` output (this skill declares `text` only), the file
name's extension must match its kind, and `size_bytes` must be a non-negative
integer. A missing manifest, a symlink or a non-regular file, more than 20
artifacts, or more than 600 MiB in total is rejected. Compute `size_bytes` and
`sha256` from the file you wrote (`stat -c %s` and `sha256sum`).

## Failure behavior

- Missing a required input: ask on the issue this run was claimed from —
  `multica issue comment add <issue id> --content-file ./question.md` — then end
  this round. Declare it in the manifest: `"outcome": "needs_input"` with
  `"artifacts": []`. Write no files and call no provider: anything you store is
  counted as an artifact and billed.
- A response whose `x-api-status-code` is anything other than `20000000`,
  including a missing header: report it, call `.../asr.recognize/finish` with
  `{"state":"failed","error_code":"provider_failed"}`, and write no manifest.
- A recording above 100 MiB, longer than two hours, or with an unsupported video
  codec: report it and stop. Do not submit a truncated recording as if it were
  the whole one.
- Never submit a second recognition request for the same task.
- Never claim completion unless the transcript file exists and the manifest
  names it.
