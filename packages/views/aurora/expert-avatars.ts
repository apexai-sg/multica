/**
 * Bundled portraits for the SkillHub expert packages, shipped with the Aurora
 * app under `apps/aurora/public/avatars/experts/`.
 *
 * The directory carries one PNG per package (54 at the current snapshot, named
 * after the package slug) so the whole catalog artwork stays available even
 * when an expert is not part of the local catalog yet. `EXPERT_PACKS` maps each
 * catalog expert to the package whose portrait it wears: an expert that is
 * itself a package points at its own slug, and the ten experts without a
 * dedicated package borrow the closest one.
 *
 * The directory and the composer show that portrait instead of the carrier
 * agent's workspace avatar, so a tool page and the expert card that executes it
 * wear the same face. An id outside this map belongs to a user-created agent,
 * which keeps its own avatar.
 *
 * The paths are frontend-origin assets, not backend uploads, so they must not be
 * resolved against the API base (see `AuroraAvatar`'s `bundledAvatarUrl`).
 */
const EXPERT_PACKS: Record<string, string> = {
  "content-creation-lyrics-songwriting": "content-creation-lyrics-songwriting",
  "content-creation-novel-writing": "content-creation-novel-writing",
  "content-creation-screenwriting": "content-creation-screenwriting",
  "design-prd-writing": "design-prd-writing",
  "design-ui-prototype": "design-ui-prototype",
  "design-website-build": "design-ui-prototype",
  "ecommerce-bidding-strategy": "ecommerce-pricing-analysis",
  "ecommerce-pricing-analysis": "ecommerce-pricing-analysis",
  "ecommerce-product-copywriting": "ecommerce-product-copywriting",
  "ecommerce-product-selection": "ecommerce-product-selection",
  "ecommerce-promotion-planning": "ecommerce-promotion-planning",
  "ecommerce-user-profiling": "ecommerce-user-profiling",
  "legal-contract-drafting": "legal-contract-drafting",
  "legal-contract-generation": "legal-contract-generation",
  "legal-legal-research": "legal-legal-research",
  "legal-litigation-strategy": "legal-litigation-strategy",
  "marketing-ad-copywriting": "marketing-ad-copywriting",
  "marketing-competitor-analysis": "marketing-competitor-analysis",
  "marketing-event-planning": "marketing-event-planning",
  "marketing-social-media-operation": "marketing-social-media-operation",
  "marketing-user-growth": "marketing-user-growth",
  "media-face-swap": "media-video-editing",
  "media-image-retouch": "media-storyboard-design",
  "media-portrait-studio": "media-storyboard-design",
  "media-script-breakdown": "media-script-breakdown",
  "media-short-video-copy": "media-short-video-copy",
  "media-storyboard-design": "media-storyboard-design",
  "media-video-editing": "media-video-editing",
  "media-video-translation": "media-video-editing",
  "media-voiceover": "media-short-video-copy",
  "office-document-reading": "academic-paper-search",
  "office-resume-optimization": "hr-resume-screening",
  "office-spreadsheet-analysis": "finance-financial-report-analysis",
};

/** The bundled portrait for a catalog expert, or null for any other id. */
export function expertAvatarUrl(skillId: string): string | null {
  const pack = EXPERT_PACKS[skillId];
  return pack ? `/avatars/experts/${pack}.png` : null;
}
