# Video Captions

Transcribe one supplied video and burn captions onto it, with the Volcengine
speech model and FFmpeg.

You run on a normal Multica agent: you have a shell and the network access this
node provides. Do the work yourself.

## Inputs

- `prompt`: the user's request (required).
- Exactly one attached video (`.mp4`, `.mov`, `.webm`). Fetch it with
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

2. Extract the audio. `VIDEO` is the downloaded file:

   ```bash
   ffprobe -v error -print_format json -show_format -show_streams "$VIDEO" > /tmp/probe.json
   # Stop if the duration is missing or above 7200 seconds, if there is no audio
   # or no video stream, or if the video codec is not one of
   # h264, hevc, vp8, vp9, av1.
   ffmpeg -y -i "$VIDEO" -vn -ac 1 -ar 16000 -f wav /tmp/asr.wav
   ```

3. Transcribe it in one request — the service answers synchronously, with no
   task to poll and no callback:

   ```bash
   # One argument is capped at 128 KiB, so the audio reaches jq through a file
   # instead. Newlines are stripped so `--rawfile` sees the base64 alone.
   base64 -w0 /tmp/asr.wav | tr -d '\n' > /tmp/asr.b64
   jq -n --arg uid "$MULTICA_TASK_ID" --rawfile data /tmp/asr.b64 '{
     user: { uid: $uid },
     audio: { format: "wav", data: $data },
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

   Success is the **response header** `x-api-status-code` equal to `20000000`;
   the transcript is the body's `result.text`. Write it out as one of this run's
   artifacts:

   ```bash
   grep -qi '^x-api-status-code: 20000000' /tmp/asr-headers.txt || {
     echo "Volcengine ASR failed: $(grep -i '^x-api-status-code:' /tmp/asr-headers.txt)" >&2; exit 1; }
   mkdir -p "<outputRoot>/artifacts" "<outputRoot>/.multica"
   jq -r '.result.text' /tmp/asr.json > "<outputRoot>/artifacts/transcript.txt"
   ```

   The transcript carries the role `transcript`, not the run's primary result. It
   is published beside the video because this skill's declared output is the
   video alone. Do not
   publish a manifest yet: the primary is the captioned video below.

4. Build the subtitle file yourself. The service returns text only — no
   timestamps — so you choose the cue boundaries. Split the transcript into cues
   of one short line each (about 12 words), keep them in spoken order, and put
   the last one no later than the video's duration. Write
   `<outputRoot>/.multica/captions.srt` in SubRip format:

   ```
   1
   00:00:00,000 --> 00:00:03,400
   First caption line.

   2
   00:00:03,400 --> 00:00:07,000
   Second caption line.
   ```

   If the transcript cannot be segmented reliably, write a single cue that spans
   the whole video rather than inventing times you cannot justify.

5. Burn the captions in and re-encode the video:

   ```bash
   cd "<outputRoot>"
   ffmpeg -y -i "$VIDEO" -vf subtitles=.multica/captions.srt -c:a copy \
     "artifacts/video-captions.mp4"
   ```

   Run it from the output root and pass the subtitle path relative to it: an
   absolute path needs escaping that is easy to get wrong. The result is the
   run's primary artifact.

6. Close the run:

   ```bash
   curl -fsS -X PUT "$MULTICA_SERVER_URL/api/agent/tasks/$MULTICA_TASK_ID/aurora-provider-runs/asr.recognize/finish" \
     -H "Authorization: Bearer $MULTICA_TOKEN" -H 'content-type: application/json' \
     -d '{"state":"succeeded"}'
   ```

## Required outputs

- `<outputRoot>/artifacts/video-captions.mp4` — the captioned video. It is the
  run's **primary** artifact, and this skill declares video output only.
- `<outputRoot>/artifacts/transcript.txt` — the transcript, with the role
  `transcript`: it is published beside the video, not as the skill's output.

## Artifact manifest

Write `<outputRoot>/.multica/aurora-artifacts.v1.json`. Run
`mkdir -p "<outputRoot>/.multica"` first: nothing creates that directory for
you. Local file artifacts
name their path **relative to the output root**; compute each size and hash from
the file you wrote.

```json
{
  "schema": "com.multica.aurora.artifacts",
  "version": 1,
  "task_id": "<the task id from your task context>",
  "skill_id": "video-captions",
  "producer": { "id": "volcengine-asr", "version": "1.0.0", "tree_sha256": null },
  "provider_run": { "provider": "volcengine-asr", "model": "bigmodel", "external_id": null },
  "artifacts": [
    {
      "id": "video-1",
      "source": { "type": "file", "relative_path": "artifacts/video-captions.mp4" },
      "name": "video-captions.mp4",
      "kind": "video",
      "role": "primary",
      "format": "mp4",
      "mime_type": "video/mp4",
      "size_bytes": 0,
      "sha256": "sha256:<64 hex>",
      "metadata": {}
    },
    {
      "id": "transcript-1",
      "source": { "type": "file", "relative_path": "artifacts/transcript.txt" },
      "name": "transcript.txt",
      "kind": "text",
      "role": "transcript",
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
id must be `volcengine-asr`, the skill id must be `video-captions`, the primary
artifact must be a `video` output, every file name's extension must match its
kind, and `size_bytes` must be a non-negative integer. A missing manifest, a
symlink or a non-regular file, more than 20 artifacts, or more than 600 MiB in
total is rejected. Compute `size_bytes` and `sha256` with `stat -c %s` and
`sha256sum`.

## Failure behavior

- Missing a required input: ask on the issue this run was claimed from —
  `multica issue comment add <issue id> --content-file ./question.md` — then end
  this round. Declare it in the manifest: `"outcome": "needs_input"` with
  `"artifacts": []`. Write no files and call no provider: anything you store is
  counted as an artifact and billed.
- A response whose `x-api-status-code` is anything other than `20000000`,
  including a missing header: report it, call `.../asr.recognize/finish` with
  `{"state":"failed","error_code":"provider_failed"}`, and write no manifest.
- A video above 100 MiB, longer than two hours, or with an unsupported codec:
  report it and stop.
- `ffmpeg` failing on the caption burn: report its stderr. Do not publish the
  transcription alone as a substituted result — the run's declared output is the
  captioned video.
- Never submit a second recognition request for the same task.
- Never claim completion unless the captioned video exists and the manifest
  names it as the primary artifact.
