/**
 * Bundled expert portraits shipped with the Aurora app under
 * `apps/aurora/public/avatars/experts/`.
 *
 * The catalog id of an Aurora expert is its slug, and the app ships one PNG per
 * catalog expert. The directory and the composer show that portrait instead of
 * the carrier agent's workspace avatar, so a tool page and the expert card that
 * executes it wear the same face. An id outside this set belongs to a
 * user-created agent, which keeps its own avatar.
 *
 * The paths are frontend-origin assets, not backend uploads, so they must not be
 * resolved against the API base (see `AuroraAvatar`'s `bundledAvatarUrl`).
 */
const EXPERT_IDS = new Set([
  "content-creation-lyrics-songwriting",
  "content-creation-novel-writing",
  "content-creation-screenwriting",
  "design-prd-writing",
  "design-ui-prototype",
  "design-website-build",
  "ecommerce-bidding-strategy",
  "ecommerce-pricing-analysis",
  "ecommerce-product-copywriting",
  "ecommerce-product-selection",
  "ecommerce-promotion-planning",
  "ecommerce-user-profiling",
  "legal-contract-drafting",
  "legal-contract-generation",
  "legal-legal-research",
  "legal-litigation-strategy",
  "marketing-ad-copywriting",
  "marketing-competitor-analysis",
  "marketing-event-planning",
  "marketing-social-media-operation",
  "marketing-user-growth",
  "media-face-swap",
  "media-image-retouch",
  "media-portrait-studio",
  "media-script-breakdown",
  "media-short-video-copy",
  "media-storyboard-design",
  "media-video-editing",
  "media-video-translation",
  "media-voiceover",
  "office-document-reading",
  "office-resume-optimization",
  "office-spreadsheet-analysis",
]);

/** The bundled portrait for a catalog expert, or null for any other id. */
export function expertAvatarUrl(skillId: string): string | null {
  return EXPERT_IDS.has(skillId) ? `/avatars/experts/${skillId}.png` : null;
}
