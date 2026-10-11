// @vitest-environment node
import { readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { RESOURCES } from "./index";
import { createInstance } from "i18next";

// Schema-level guard: every key in the EN bundle must have a counterpart
// in every non-English bundle and vice-versa. Catches retrofit drift where a
// new EN key lands without a translated key, which would silently fall back
// to the English string in production.
//
// i18next plural rule: EN uses `_one` + `_other`; zh only uses `_other`
// because Chinese has no grammatical number. Normalize both forms to
// `_other` before comparing so a `{ key_one, key_other }` pair in EN
// matches a single `{ key_other }` in zh. French also has a legitimate
// `_many` category for exact millions; it belongs to the same key family.

// Derive the canonical namespace list from disk so the test fails if a JSON
// file ships without a matching RESOURCES entry. Without this guard the test
// would still pass when EN and a non-English bundle skip a namespace (e.g. issues +
// agents both unregistered), since the iteration happens over RESOURCES.en
// itself — that's a tautology, not parity.
const LOCALES_DIR = dirname(fileURLToPath(import.meta.url));

function jsonNamespacesIn(locale: string): string[] {
  return readdirSync(resolve(LOCALES_DIR, locale))
    .filter((name) => name.endsWith(".json"))
    .map((name) => name.replace(/\.json$/, ""))
    .sort();
}

type Json = Record<string, unknown>;

function flattenKeys(obj: unknown, prefix = ""): string[] {
  if (obj === null || typeof obj !== "object") return [prefix];
  const entries = Object.entries(obj as Json);
  if (entries.length === 0) return [];
  return entries.flatMap(([k, v]) =>
    flattenKeys(v, prefix ? `${prefix}.${k}` : k),
  );
}

function normalizePlural(key: string): string {
  return key.replace(/_(one|many|other)$/, "_count");
}

function keySet(bundle: Record<string, unknown>): Set<string> {
  return new Set(flattenKeys(bundle).map(normalizePlural));
}

const en = RESOURCES.en;
const translatedLocales = Object.keys(RESOURCES).filter(
  (locale) => locale !== "en",
);

describe("Aurora directory plural families", () => {
  it.each([
    ["en", 0, "0 skills available", "skills runnable", "skills in the catalog"],
    ["en", 1, "1 skill available", "skill runnable", "skill in the catalog"],
    ["en", 3, "3 skills available", "skills runnable", "skills in the catalog"],
    ["zh-Hans", 0, "0 个 skill 可用", "个 skill 可运行", "个 skill"],
    ["zh-Hans", 1, "1 个 skill 可用", "个 skill 可运行", "个 skill"],
    ["zh-Hans", 3, "3 个 skill 可用", "个 skill 可运行", "个 skill"],
    ["ja", 0, "0 件が利用可能", "件が実行可能", "件のスキル"],
    ["ja", 1, "1 件が利用可能", "件が実行可能", "件のスキル"],
    ["ja", 3, "3 件が利用可能", "件が実行可能", "件のスキル"],
    ["ko", 0, "0개 사용 가능", "개 실행 가능", "개 스킬"],
    ["ko", 1, "1개 사용 가능", "개 실행 가능", "개 스킬"],
    ["ko", 3, "3개 사용 가능", "개 실행 가능", "개 스킬"],
    ["fr", 0, "0 compétence disponible", "compétence exécutable", "compétence au catalogue"],
    ["fr", 1, "1 compétence disponible", "compétence exécutable", "compétence au catalogue"],
    ["fr", 3, "3 compétences disponibles", "compétences exécutables", "compétences au catalogue"],
    ["fr", 1000000, "1000000 compétences disponibles", "compétences exécutables", "compétences au catalogue"],
  ] as const)("renders %s count %i without fallback or duplicated numerals", async (locale, count, visible, runnable, total) => {
    const i18n = createInstance();
    await i18n.init({ lng: locale, fallbackLng: false, resources: RESOURCES });
    expect(i18n.t(($) => $.directory.visible_count, { ns: "aurora", count })).toBe(visible);
    expect(i18n.t(($) => $.directory.stats_runnable, { ns: "aurora", count })).toBe(runnable);
    expect(i18n.t(($) => $.directory.stats_total, { ns: "aurora", count })).toBe(total);
  });
  const families = ["visible_count", "stats_runnable", "stats_total"];
  const categories = { en: ["one", "other"], "zh-Hans": ["other"], ja: ["other"], ko: ["other"], fr: ["many", "one", "other"] };
  for (const [locale, suffixes] of Object.entries(categories)) {
    it(`${locale} supplies exactly its grammatical count forms`, () => {
      const keys = flattenKeys(RESOURCES[locale as keyof typeof RESOURCES].aurora);
      for (const family of families) {
        const base = `directory.${family}`;
        expect(keys.filter((key) => key === base || key.startsWith(base + "_")).sort())
          .toEqual(suffixes.map((suffix) => base + "_" + suffix).sort());
      }
    });
  }
});

describe("locale bundle parity", () => {
  it("registers every JSON file in RESOURCES (EN)", () => {
    expect(Object.keys(en).sort()).toEqual(jsonNamespacesIn("en"));
  });

  for (const locale of translatedLocales) {
    const bundle = RESOURCES[locale as keyof typeof RESOURCES];

    it(`declares the same namespaces in EN and ${locale}`, () => {
      expect(Object.keys(bundle).sort()).toEqual(Object.keys(en).sort());
    });

    it(`registers every JSON file in RESOURCES (${locale})`, () => {
      expect(Object.keys(bundle).sort()).toEqual(jsonNamespacesIn(locale));
    });

    for (const ns of Object.keys(en)) {
      it(`${ns}: ${locale} covers every EN key`, () => {
        const enKeys = keySet(en[ns] ?? {});
        const translatedKeys = keySet(bundle[ns] ?? {});
        const missing = [...enKeys].filter((k) => !translatedKeys.has(k));
        expect(missing).toEqual([]);
      });

      it(`${ns}: EN covers every ${locale} key`, () => {
        const enKeys = keySet(en[ns] ?? {});
        const translatedKeys = keySet(bundle[ns] ?? {});
        const extra = [...translatedKeys].filter((k) => !enKeys.has(k));
        expect(extra).toEqual([]);
      });
    }
  }
});

// Dead plural-key guard: a locale whose CLDR plural rules have no `one`
// category (e.g. ja/ko/zh-Hans) resolves only `_other`, so any `_one` key in
// it is dead weight i18next never renders. Left unchecked these accumulate and
// hide bugs — a missing `_other` silently falls back while the orphan `_one`
// looks like coverage. i18next resolves plurals via Intl.PluralRules, so we
// gate on the same source of truth.
describe("dead plural-key guard", () => {
  for (const locale of translatedLocales) {
    const categories = new Intl.PluralRules(locale).resolvedOptions()
      .pluralCategories;
    if (categories.includes("one")) continue;

    const bundle = RESOURCES[locale as keyof typeof RESOURCES];
    it(`${locale} ships no dead _one keys (plural categories: ${categories.join("/")})`, () => {
      const offenders = Object.keys(bundle).flatMap((ns) =>
        flattenKeys(bundle[ns])
          .filter((key) => key.endsWith("_one"))
          .map((key) => `${ns}:${key}`),
      );
      expect(offenders).toEqual([]);
    });
  }
});
