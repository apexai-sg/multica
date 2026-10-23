import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";

import {
  buildLibrary,
  importExperts,
  importPlan,
  isManagedBrief,
  isProvenancePath,
  namespacedSlug,
  parseFrontmatter,
  renderBrief,
  rewriteSkillName,
  sanitizeSkillDir,
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

  // The snapshot tests are about the upstream tree only. An explicit empty
  // local index keeps the repository's hand-written experts out of them.
  write("local-index.json", JSON.stringify({ experts: [] }));

  write(
    "skillhub-skillset-ecommerce-demo-a/manifest.json",
    JSON.stringify({
      label: { zh: "演示专家", en: "Demo Expert" },
      description: { zh: "中文说明", en: "English description" },
      quick_prompts: [
        {
          title: { zh: "第一步", en: "Step one" },
          prompt: { zh: "请帮我做第一步。", en: "Help me with step one." },
        },
      ],
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
  write("skillhub-skillset-ecommerce-demo-a/skills/alpha/_meta.json", JSON.stringify({ slug: "alpha" }));
  write("skillhub-skillset-ecommerce-demo-a/skills/alpha/references/notes.md", "# notes" + "\n");
  write("skillhub-skillset-ecommerce-demo-a/skills/alpha/.learnings/CHARACTERS.md", "# characters" + "\n");
  write("skillhub-skillset-ecommerce-demo-a/skills/alpha/scripts/__init__.py", "# package" + "\n");
  // An upstream download that failed leaves an empty directory behind.
  mkdirSync(join(dir, "skillhub-skillset-ecommerce-demo-a/skills/empty-sub"), { recursive: true });

  write(
    "skillhub-skillset-legal-demo-b/manifest.json",
    JSON.stringify({
      label: { zh: "法律演示", en: "Legal Demo" },
      description: { zh: "法律法规" },
      quick_prompts: [
        {
          title: { zh: "起草合同", en: "Draft a contract" },
          prompt: { zh: "请帮我起草一份合同。", en: "Draft a contract for me." },
        },
      ],
    }),
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

  const { experts, files } = await buildLibrary(dir, join(dir, "local-index.json"));
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
  assert.equal(demo.subskills[0].name, "ecommerce-demo-a-alpha");
  assert.equal(demo.subskills[0].name, sanitizeSkillDir(demo.subskills[0].name));
  assert.deepEqual(
    demo.subskills[0].files.map((file) => file.path),
    [".learnings/CHARACTERS.md", "SKILL.md", "references/notes.md", "scripts/__init__.py", "scripts/run.sh"],
  );

  const subSkill = files.get("aurora-experts/ecommerce-demo-a/ecommerce-demo-a-alpha/SKILL.md");
  assert.match(subSkill, /^name: ecommerce-demo-a-alpha$/m);
  assert.equal(
    files.get("aurora-experts/ecommerce-demo-a/ecommerce-demo-a-alpha/scripts/run.sh"),
    "#!/bin/sh" + "\n" + "echo hi" + "\n",
  );
  assert.ok(files.has("server/internal/aurora/experts/index.json"));
  assert.ok(
    files.has("aurora-experts/ecommerce-demo-a/ecommerce-demo-a-alpha/_meta.json"),
    "_meta.json stays in the checkout for provenance",
  );
  assert.equal(files.has("server/internal/aurora/experts/ecommerce-demo-a/subskills/alpha/SKILL.md"), false);

  const index = JSON.parse(files.get("server/internal/aurora/experts/index.json"));
  assert.equal(index.schema, 2);
  assert.equal(index.experts[0].subskills[0].files, undefined);
  assert.deepEqual(index.experts[1].missing_subskills, []);
  assert.deepEqual(index.experts[0].quick_prompts, [
    {
      label_zh: "第一步",
      label_en: "Step one",
      prompt_zh: "请帮我做第一步。",
      prompt_en: "Help me with step one.",
    },
  ]);

  const plan = importPlan(files);
  const planned = new Set(plan.map((file) => file.path));
  assert.equal(planned.has("ecommerce-demo-a/ecommerce-demo-a-alpha/_meta.json"), false);
  for (const [path] of files) {
    if (!path.startsWith("aurora-experts/")) {
      continue;
    }
    const rel = path.slice("aurora-experts/".length);
    if (isProvenancePath(rel)) {
      assert.equal(planned.has(rel), false, rel + " must stay out of the import");
      continue;
    }
    assert.equal(planned.has(rel), true, rel + " is missing from the import plan");
  }
  const alpha = plan.find((file) => file.path.endsWith("/.learnings/CHARACTERS.md"));
  assert.equal(alpha.content, "# characters" + "\n");
});

test("buildLibrary is deterministic", async (t) => {
  const dir = scaffold();
  t.after(() => rmSync(dir, { recursive: true, force: true }));

  const first = await buildLibrary(dir, join(dir, "local-index.json"));
  const second = await buildLibrary(dir, join(dir, "local-index.json"));
  assert.deepEqual([...first.files.keys()], [...second.files.keys()]);
  for (const [path, content] of first.files) {
    assert.equal(second.files.get(path), content, path + " differs between runs");
  }
});

test("the generated brief satisfies the Aurora workflow contract", async (t) => {
  const dir = scaffold();
  t.after(() => rmSync(dir, { recursive: true, force: true }));

  const { experts } = await buildLibrary(dir, join(dir, "local-index.json"));
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
  assert.ok(brief.includes(".claude/skills/ecommerce-demo-a-alpha/SKILL.md"));
  assert.ok(brief.includes("使用 **Alpha** 完成。"));
});

test("validateBriefs rejects a broker-shaped token", async (t) => {
  const dir = scaffold();
  t.after(() => rmSync(dir, { recursive: true, force: true }));

  const { experts } = await buildLibrary(dir, join(dir, "local-index.json"));
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

test("importExperts copies every non-provenance file and keeps directory names", async (t) => {
  const dir = scaffold();
  const dest = mkdtempSync(join(tmpdir(), "aurora-runtime-"));
  t.after(() => {
    rmSync(dir, { recursive: true, force: true });
    rmSync(dest, { recursive: true, force: true });
  });

  const { files } = await buildLibrary(dir, join(dir, "local-index.json"));
  const source = mkdtempSync(join(tmpdir(), "aurora-skills-"));
  t.after(() => rmSync(source, { recursive: true, force: true }));
  const { mkdirSync: mkdir, writeFileSync: write } = await import("node:fs");
  for (const [path, content] of files) {
    if (!path.startsWith("aurora-experts/")) {
      continue;
    }
    const rel = path.slice("aurora-experts/".length);
    const full = join(source, rel);
    mkdir(dirname(full), { recursive: true });
    write(full, content);
  }

  const copied = await importExperts(dest, source);
  assert.deepEqual(copied, importPlan(files).map((file) => file.path));
  assert.equal(copied.some((path) => path.endsWith("/_meta.json")), false);
  assert.equal(
    copied.includes("ecommerce-demo-a/ecommerce-demo-a-alpha/.learnings/CHARACTERS.md"),
    true,
  );
  const script = join(dest, "ecommerce-demo-a/ecommerce-demo-a-alpha/scripts/run.sh");
  assert.equal(statSync(script).mode & 0o111, 0o111);
  const notes = join(dest, "ecommerce-demo-a/ecommerce-demo-a-alpha/references/notes.md");
  assert.equal(statSync(notes).mode & 0o111, 0);
});

test("merges locally authored experts into the generated index", async (t) => {
  const dir = scaffold();
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const local = join(dir, "local.json");
  writeFileSync(
    local,
    JSON.stringify({
      experts: [
        {
          slug: "media-demo-local",
          name_zh: "本地演示",
          name_en: "Local Demo",
          scene: "media",
          description_zh: "本地",
          description_en: "Local",
          quick_prompts: [{ label_zh: "开始", label_en: "Start", prompt_zh: "开始", prompt_en: "Start" }],
        },
      ],
    }),
  );

  const { files } = await buildLibrary(dir, local);
  const index = JSON.parse(files.get("server/internal/aurora/experts/index.json"));
  assert.ok(index.experts.some((e) => e.slug === "media-demo-local"));
  assert.ok(index.experts.some((e) => e.slug === "ecommerce-demo-a"));

  const merged = index.experts.find((e) => e.slug === "media-demo-local");
  assert.deepEqual(merged.missing_subskills, []);
  assert.deepEqual(merged.subskills, []);
  assert.deepEqual(merged.quick_prompts, [
    { label_zh: "开始", label_en: "Start", prompt_zh: "开始", prompt_en: "Start" },
  ]);
  assert.equal(
    files.has("server/internal/aurora/experts/media-demo-local/workflow.md"),
    false,
    "a hand-written brief is not generated",
  );
});

test("a locally authored brief is not compared by --check", () => {
  assert.equal(isManagedBrief("media-demo-local/workflow.md", new Set(["media-demo-local"])), false);
  assert.equal(isManagedBrief("ecommerce-demo-a/workflow.md", new Set(["media-demo-local"])), true);
});
