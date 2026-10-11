/**
 * The design's avatar: initials on a pastel tile, with an optional state dot.
 *
 * Skills ship no portrait art, so the tint is derived from the id. The same
 * skill therefore keeps the same colour on every surface that names it — the
 * card, the drawer header, the hero orbit — which is what makes the tile read
 * as an identity rather than as decoration.
 */
export type AuroraAvatarTint =
  | "coral"
  | "violet"
  | "rose"
  | "mint"
  | "amber"
  | "blue"
  | "rust"
  | "strawberry";

const TINTS: AuroraAvatarTint[] = [
  "coral",
  "violet",
  "rose",
  "mint",
  "amber",
  "blue",
  "rust",
  "strawberry",
];

/**
 * A stable tint for an id.
 *
 * A hash rather than the catalog index: an index would repaint every tile the
 * moment the server reorders the catalog, and the colour has to survive a
 * re-render even if the list it came from does not.
 */
export function avatarTint(id: string): AuroraAvatarTint {
  return TINTS[hashId(id) % TINTS.length]!;
}

/**
 * A stable index in 1..6 for the six pastel surfaces (`aurora-tint-1` … `-6`),
 * independently of the eight avatar tint names.
 */
export function tintIndex(id: string): number {
  return (hashId(id) % 6) + 1;
}

function hashId(id: string): number {
  let hash = 0;
  for (let position = 0; position < id.length; position += 1) {
    hash = (hash * 31 + id.charCodeAt(position)) >>> 0;
  }
  return hash;
}

/** Up to two characters for the tile. */
export function avatarInitial(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) return "?";
  // Latin names read better as two initials ("Product Image" -> "PI"); a CJK
  // name is one word and one meaningful character.
  const words = trimmed.split(/\s+/).filter((word) => /^[A-Za-z0-9]/.test(word));
  const first = words[0] ?? "";
  const second = words[1] ?? "";
  if (first && second) return (first.charAt(0) + second.charAt(0)).toUpperCase();
  // Array.from splits by code point, so a surrogate pair is one character.
  return Array.from(Array.from(trimmed)[0]!.toUpperCase()).slice(0, 2).join("");
}
