# Local Aurora design preview

Run `pnpm --filter @multica/aurora preview` and open http://127.0.0.1:4311.

This local harness renders the same app-local directory, full-screen workspaces,
styles and bundled portraits as the production Next.js routes. Components live in
`../components/tools/`; portraits live in `../public/avatars/`. It contains 18 visual tool
proposals: six retained tools plus the owner's 12 new selections. The generic
office card is replaced by concrete office tools. Cards open full-page workspaces;
search and category selections survive returning to the directory. The banner
identifies the design as a preview, not connected processing. Account/balance data
in the existing shell is also a fixture, not a live balance. Proposed tool cards
do not advertise prices or service availability.

The single registry, `../components/tools/tool-definitions.ts`, supplies names, categories, upload
requirements, examples, settings and result layouts. It also feeds the fixture API.
Its choices are design proposals, not provider contracts. Existing preview links
for the five original generic tools and the face-swap page still resolve.

Open http://127.0.0.1:4311/preview/tools/face-swap for the selected design A:
two source/face upload cards, a video/image switch, and the result area. File
selection, drag/drop, replacement, removal, and media previews use local blob
URLs, revoked when replaced or unmounted. A source for each mode and the shared
face are retained while switching modes. Limits (200 MB video, 20 MB image) are
preview limits, not a provider contract. Generate only explains that a service
has not been connected; there is no simulated output or enabled download.

All other workspaces have their own source and settings controls. They accept
local images/videos/documents, validate file type and size, and allow replacement
or removal. Video/image decoding errors block the primary action. Documents are
represented by their filenames, not parsed. Required source text and uploads gate
the primary action, which explains that no real processing has occurred. Result
areas show labeled layout placeholders, never invented generated outputs.

`/preview/tools/video-transcript` adds separate speech and on-screen-text tabs.
“查看示例结果” explicitly loads built-in sample segments unrelated to uploaded
videos. Users can edit the samples, hide timestamps, copy the selected view, and
export sample TXT/SRT files. Export names label them as samples; TXT contents do
as well. Changing source/settings clears samples and status. No transcription,
OCR, video analysis or other inference is performed. Local export content and URL
cleanup are tested; download delivery still depends on the host browser.

The fixture API rejects every unhandled operation and has no network fallback.
This harness uses no real login, database, Docker, provider key, or production
endpoint. Fixture API, sample account/balance and Vite routing remain preview-only.
The approved UI now ships at `/{workspaceSlug}/skills` and
`/{workspaceSlug}/tools/{toolId}` under the existing production auth/workspace
guards. The prior backend-driven catalog remains at
`/{workspaceSlug}/skills/connected`. See [deployment handoff](../DEPLOYMENT_HANDOFF.md)
for the build, acceptance checks and backend work still needed.

## Design handoff

| Tool | Source | Settings / result layout |
| --- | --- | --- |
| 视频文案提取 | Video | Extraction scope and language; editable speech / on-screen text with time ranges |
| 图片／视频换脸 | Source image/video + face photo | Mode switch; result player/image |
| 海报制作 | Copy + optional image | Style, ratio, count; image gallery |
| 小红书图片 | Copy + optional image | Cover/interior format, style, count; ordered gallery |
| 图片精修 | Image + optional instructions | Task, background, strength; before/after area |
| 商品图制作 | Product image + optional instructions | Scene, ratio, count; image variants |
| 人像写真 | Portrait image | Style, background, ratio; portrait gallery |
| 短视频制作 | Description + optional reference image | Duration, ratio, style; player and script |
| 视频剪辑 | Video + optional instructions | Duration, ratio, captions; player and segment timeline area |
| 数字人口播 | Script + optional portrait | Example character, language, pace; player |
| 视频翻译 | Video | Source/target language, captions, voice; player and transcript comparison area |
| PPT 制作 | Topic and source text | Purpose, length, style; thumbnails and slide preview |
| Excel 助手 | XLSX/XLS/CSV + question | Task and language; table/chart/notes area |
| PDF 阅读助手 | PDF + optional question | Reading mode, length; answer and page-reference area |
| 文案创作 | Brief | Platform, tone, length; editable draft area |
| 音频配音 | Script | Voice, language, pace; audio player area |
| 网站设计助手 | Website brief | Type, style, color; desktop/mobile layout toggle |
| 简历优化 | PDF/Word + optional target role | Focus, language, length; suggestions and revised-document area |

The technical handoff must separately determine supported formats/durations,
upload storage, job progress/cancellation/errors, real output schemas, export
behavior and pricing. For video extraction, preserve separate timed speech and
OCR segments rather than mixing them into a single transcript. Current preview
limits are 200 MB/video, 20 MB/image and 30 MB/document. They are not final limits.

Checks: `pnpm --filter @multica/aurora typecheck`,
`pnpm --filter @multica/aurora exec eslint components/tools preview`,
`pnpm --filter @multica/aurora exec vitest run`, and
`pnpm --filter @multica/aurora build`.
