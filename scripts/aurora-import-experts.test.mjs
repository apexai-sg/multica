import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";

import {
  buildLibrary,
  namespacedSlug,
  parseFrontmatter,
  renderBrief,
  rewriteSkillName,
  sceneOf,
  validateBriefs,
} from "./aurora-import-experts.mjs";

function scaffold() {
  const dir = mkdtempSync(join(tmpdir(), "aurora-experts-"));
  const write = (rel, content) => {
    const full = join(dir, rel);
    mkdirSync(dirname(full), { recursive: true });
    writeFileSync(full, content);
  };

  write(
    "skillhub-skillset-ecommerce-demo-a/manifest.json",
    JSON.stringify({
      label: { zh: "演示专家", en: "Demo Expert" },
      description: { zh: "中文说明", en: "English description" },
    }),
  );
  write("skillhub-skillset-ecommerce-demo-a/SOUL.md", "# 演示专家" + "\n" + "你是演示专家。" + "\n");
  write(
    "skillhub-skillset-ecommerce-demo-a/skills/ecommerce-demo-a/SKILL.md",
    "---" + "\n" + 'scene: "ecommerce"' + "\n" + "sub_scene: demo-a" + "\n" + "---" + "\n" + "\n" +
      "# 演示工作流" + "\n" + "\n" + "## 步骤 1" + "\n" + "使用 **Alpha** 完成。" + "\n",
  );
  write(
    "skillhub-skillset-ecommerce-demo-a/skills/alpha/SKILL.md",
    "---" + "\n" + "name: alpha" + "\n" + "display_name: 甲" + "\n" +
      "display_name_en: Alpha" + "\n" + "description: d" + "\n" + "---" + "\n" + "\n" + "# Alpha" + "\n",
  );
  write("skillhub-skillset-ecommerce-demo-a/skills/alpha/scripts/run.sh", "#!/bin/sh" + "\n" + "echo hi" + "\n");
  // Vendored for provenance, never listed: go:embed cannot serve an underscore
  // name, and the Go loader would fail on the whole sub-skill if it did.
  write("skillhub-skillset-ecommerce-demo-a/skills/alpha/_meta.json", JSON.stringify({ slug: "alpha" }));
  write("skillhub-skillset-ecommerce-demo-a/skills/alpha/references/notes.md", "# notes" + "\n");
  // An upstream download that failed leaves an empty directory behind.
  mkdirSync(join(dir, "skillhub-skillset-ecommerce-demo-a/skills/empty-sub"), { recursive: true });

  write(
    "skillhub-skillset-legal-demo-b/manifest.json",
    JSON.stringify({ label: { zh: "法律演示", en: "Legal Demo" }, description: { zh: "法律法规" } }),
  );
  write("skillhub-skillset-legal-demo-b/SOUL.md", "# 法律演示" + "\n");
  write(
    "skillhub-skillset-legal-demo-b/skills/legal-demo-b/SKILL.md",
    "---" + "\n" + "name: legal-demo-b" + "\n" + "---" + "\n" + "\n" + "# 法律工作流" + "\n",
  );

  write(
    "skillhub-skillset-tech-test-automation/manifest.json",
    JSON.stringify({ label: { zh: "自动化测试专家", en: "Test Automation" } }),
  );
  write(
    "skillhub-skillset-tech-test-automation/skills/tech-test-automation/SKILL.md",
    "---" + "\n" + "name: tech-test-automation" + "\n" + "---" + "\n" + "\n" + "# ignored" + "\n",
  );

  return dir;
}

test("buildLibrary keeps the six scenes, folds the slug prefix and namespaces sub-skills", async (t) => {
  const dir = scaffold();
  t.after(() => rmSync(dir, { recursive: true, force: true }));

  const { experts, files } = await buildLibrary(dir);
  assert.deepEqual(
    experts.map((expert) => expert.slug),
    ["ecommerce-demo-a", "legal-demo-b"],
  );
  assert.deepEqual(
    experts.map((expert) => expert.scene),
    ["ecommerce", "legal"],
  );

  const demo = experts[0];
  assert.equal(demo.nameZh, "演示专家");
  assert.equal(demo.nameEn, "Demo Expert");
  assert.deepEqual(demo.MissingSubskills ?? demo.missingSubskills, ["empty-sub"]);
  assert.deepEqual(
    demo.subskills.map((sub) => sub.slug),
    ["alpha"],
  );
  assert.equal(demo.subskills[0].name, namespacedSlug("ecommerce-demo-a", "alpha"));
  assert.deepEqual(
    demo.subskills[0].files.map((file) => file.path),
    ["SKILL.md", "references/notes.md", "scripts/run.sh"],
  );

  const subSkill = files.get(
    "server/internal/aurora/experts/ecommerce-demo-a/subskills/alpha/SKILL.md",
  );
  assert.match(subSkill, /^name: ecommerce-demo-a--alpha$/m);
  assert.equal(
    files.get("server/internal/aurora/experts/ecommerce-demo-a/subskills/alpha/scripts/run.sh"),
    "#!/bin/sh" + "\n" + "echo hi" + "\n",
  );
  assert.ok(files.has("server/internal/aurora/experts/index.json"));
  assert.ok(
    files.has("server/internal/aurora/experts/ecommerce-demo-a/subskills/alpha/_meta.json"),
    "_meta.json stays vendored for provenance",
  );

  const index = JSON.parse(files.get("server/internal/aurora/experts/index.json"));
  assert.equal(index.schema, 1);
  assert.deepEqual(index.experts[1].missing_subskills, []);
  assert.equal(index.experts[0].subskills[0].files.length, 3);
});

test("buildLibrary is deterministic", async (t) => {
  const dir = scaffold();
  t.after(() => rmSync(dir, { recursive: true, force: true }));

  const first = await buildLibrary(dir);
  const second = await buildLibrary(dir);
  assert.deepEqual([...first.files.keys()], [...second.files.keys()]);
  for (const [path, content] of first.files) {
    assert.equal(second.files.get(path), content, path + " differs between runs");
  }
});

test("the generated brief satisfies the Aurora workflow contract", async (t) => {
  const dir = scaffold();
  t.after(() => rmSync(dir, { recursive: true, force: true }));

  const { experts } = await buildLibrary(dir);
  assert.deepEqual(validateBriefs(experts), []);

  const brief = renderBrief(experts[0]);
  for (const section of [
    "## Inputs",
    "## Steps",
    "## Required outputs",
    "## Artifact manifest",
    "## Failure behavior",
  ]) {
    assert.ok(brief.includes(section), "missing " + section);
  }
  assert.ok(brief.includes("com.multica.aurora.artifacts"));
  assert.ok(brief.includes("ecommerce-demo-a"));
  assert.ok(brief.includes(".claude/skills/ecommerce-demo-a--alpha/SKILL.md"));
  assert.ok(brief.includes("使用 **Alpha** 完成。"));
});

test("validateBriefs rejects a broker-shaped token", async (t) => {
  const dir = scaffold();
  t.after(() => rmSync(dir, { recursive: true, force: true }));

  const { experts } = await buildLibrary(dir);
  const broken = structuredClone(experts);
  broken[0].workflow = "call aurora.seedream_generate now";
  const problems = validateBriefs(broken);
  assert.equal(problems.length, 1);
  assert.match(problems[0], /aurora\.seedream_generate/);
});

test("frontmatter helpers normalize line endings and rewrite the name", () => {
  const crlf = "---" + "\r\n" + "name: alpha" + "\r\n" + "---" + "\r\n" + "\r\n" + "# body";
  const parsed = parseFrontmatter(crlf);
  assert.equal(parsed.frontmatter, "name: alpha");
  assert.equal(parsed.body, "# body");

  assert.equal(rewriteSkillName(crlf, "demo--alpha").split("\n")[1], "name: demo--alpha");
  assert.equal(rewriteSkillName("# no frontmatter", "demo--beta").split("\n")[1], "name: demo--beta");
});

test("sceneOf prefers the frontmatter and falls back to the slug prefix", () => {
  assert.equal(sceneOf("content-creation-novel-writing", "content-creation"), "content-creation");
  assert.equal(sceneOf("legal-demo-b", ""), "legal");
  assert.equal(sceneOf("tech-test-automation", ""), "");
});
