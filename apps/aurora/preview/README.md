# Local Aurora design preview

Run `pnpm --filter @multica/aurora preview` and open http://127.0.0.1:4311.

This entry point renders the production Aurora shell, directory, and composer with
in-memory example data. Cards open full-page workspaces, and the browser back
button returns to the directory. Search, output filters, the mobile navigation
sheet, and conversation starters are interactive. The banner identifies all
capabilities, illustrated avatars, and credits as examples, not the deployed workspace.

Open http://127.0.0.1:4311/preview/tools/face-swap for the selected design A:
two source/face upload cards, a video/image switch, and the result area. File
selection, drag/drop, replacement, removal, and media previews use local blob
URLs, revoked when replaced or unmounted. A source for each mode and the shared
face are retained while switching modes. Limits (200 MB video, 20 MB image) are
preview limits, not a provider contract. Generate only explains that a service
has not been connected; there is no simulated output or enabled download.

Other submissions and uploads are intercepted, including dropped files. The
fixture API rejects every unhandled operation and has no network fallback.
No login, database, Docker, provider key, or production endpoint is used. The
preview is separate from Next.js routes and does not ship in the production app.
Production directory/composer defaults remain unchanged; their optional selection
callback/page presentation let this host demonstrate navigation. Use the deployed
environment to verify real execution after visual acceptance and service integration.
