// @vitest-environment node
import { describe, expect, it } from "vitest";
import { avatarInitial, avatarTint, tintIndex } from "./aurora-avatar-helpers";

describe("stable avatar colours", () => {
  // Literal outcomes pin the unsigned 31-based hash, not catalog order.
  it.each<[string, string, number]>([
    ["", "coral", 1],
    ["a", "violet", 2],
    ["b", "rose", 3],
    ["c", "mint", 4],
    ["d", "amber", 5],
    ["e", "blue", 6],
    ["f", "rust", 1],
    ["g", "strawberry", 2],
    ["ab", "violet", 4],
    ["ba", "strawberry", 4],
    ["poster", "blue", 4],
    ["xhs-image", "violet", 6],
    ["product-image", "blue", 4],
    ["text-image", "mint", 6],
  ])("keeps %j on its assigned tint and pastel surface", (id, tint, index) => {
    expect(avatarTint(id)).toBe(tint);
    expect(tintIndex(id)).toBe(index);
  });
});

describe("avatarInitial", () => {
  it.each([
    ["Poster", "P"],
    [" product   image studio ", "PI"],
    ["海报制作", "海"],
    ["", "?"],
    ["   ", "?"],
    ["🎨 Studio", "🎨"],
    ["ﬃ", "FF"],
  ])("renders up to two characters for %j", (name, initial) => {
    expect(avatarInitial(name)).toBe(initial);
  });
});
