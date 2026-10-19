# Local Aurora design preview

Run `pnpm --filter @multica/aurora preview` and open http://127.0.0.1:4311.

This entry point renders the production Aurora shell, directory, and composer with
in-memory example data. Search, output filters, the mobile navigation sheet, and
conversation starters are interactive. The banner identifies all capabilities,
illustrated avatars, and credits as examples, not the deployed workspace.

Submission and uploads are intercepted. The fixture API rejects every unhandled
operation and has no network fallback. No login, database, Docker, provider key,
or production endpoint is used. It is separate from Next.js routes and does not
ship in the production app. Use the deployed environment to verify real task
execution after visual acceptance.
