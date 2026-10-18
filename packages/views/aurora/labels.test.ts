// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { AuroraSkill } from "@multica/core/aurora";
import {
  generationStatusLabel,
  ledgerKindLabel,
  skillDisplayName,
  skillDisplayNamesById,
} from "./labels";

function skill(overrides: Partial<AuroraSkill> = {}): AuroraSkill {
  return {
    id: "poster",
    name: "海报制作",
    nameEn: "Poster",
    category: "image",
    credits: 76,
    input: ["text", "image"],
    output: ["image"],
    featured: true,
    available: true,
    attachments: [],
    avatarUrl: "",
    ...overrides,
  };
}

describe("generationStatusLabel", () => {
  it("maps each status the server derives from the enqueued task", () => {
    expect(generationStatusLabel("queued")).toBe("queued");
    expect(generationStatusLabel("running")).toBe("running");
    expect(generationStatusLabel("completed")).toBe("completed");
    expect(generationStatusLabel("failed")).toBe("failed");
  });

  it("falls back for a status a newer server introduces", () => {
    // The schema keeps `status` a plain string, so an unrecognised value has to
    // land somewhere rather than rendering an undefined label.
    expect(generationStatusLabel("cancelled")).toBe("unknown");
  });

  it("falls back for a missing status", () => {
    expect(generationStatusLabel(undefined)).toBe("unknown");
  });
});

describe("ledgerKindLabel", () => {
  it("maps the four kinds the ledger writes today", () => {
    expect(ledgerKindLabel("topup")).toBe("topup");
    expect(ledgerKindLabel("deduction")).toBe("deduction");
    expect(ledgerKindLabel("refund")).toBe("refund");
    expect(ledgerKindLabel("adjustment")).toBe("adjustment");
  });

  it("maps the expiry kind Plan 5 adds", () => {
    expect(ledgerKindLabel("expire")).toBe("expire");
  });

  it("falls back for a kind this build has never seen", () => {
    expect(ledgerKindLabel("chargeback")).toBe("unknown");
  });
});

describe("skillDisplayName", () => {
  it("shows the catalog's Chinese name to a Chinese reader", () => {
    expect(skillDisplayName(skill(), "zh-Hans")).toBe("海报制作");
  });

  it("shows the English name to every other reader", () => {
    // The catalog pairs a Chinese display name with an English one. A French or
    // Korean reader has no use for the Chinese string, and the catalog has no
    // third translation to offer.
    expect(skillDisplayName(skill(), "fr")).toBe("Poster");
    expect(skillDisplayName(skill(), "en")).toBe("Poster");
  });

  it("falls back to the Chinese name when the English one is missing", () => {
    // `nameEn` defaults to "" in the schema, so a catalog entry that ships
    // without one must still render something rather than a blank label.
    expect(skillDisplayName(skill({ nameEn: "" }), "en")).toBe("海报制作");
  });
});

describe("skillDisplayNamesById", () => {
  it("keys every skill by id, in the language the caller is reading", () => {
    const skills = [skill(), skill({ id: "ppt", name: "PPT 制作", nameEn: "PPT" })];

    expect(skillDisplayNamesById(skills, "fr").get("poster")).toBe("Poster");
    expect(skillDisplayNamesById(skills, "zh-Hans").get("ppt")).toBe("PPT 制作");
  });

  it("does not name an id the catalog does not carry", () => {
    // The screens that hold an id must be able to tell "the catalog dropped
    // this entry" from "the catalog never loaded", so a miss is absent rather
    // than an invented label.
    expect(skillDisplayNamesById([skill()], "en").get("retired")).toBeUndefined();
  });
});
