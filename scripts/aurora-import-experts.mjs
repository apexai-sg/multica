#!/usr/bin/env node
// Generate the Aurora expert library from an octop SkillHub snapshot.
//
// The snapshot is the expert_market directory of an Octop instance; see
// docs/superpowers/specs/2026-10-14-aurora-octop-expert-library-design.md.
// This script owns two trees:
//
//   server/internal/aurora/experts/<slug>/workflow.md
//   server/internal/aurora/experts/index.json
//       the Aurora brief and the catalog index. The API embeds only these.
//
//   aurora-experts/<slug>/<dir>/**
//       sub-skill bodies, references and scripts. <dir> is the fixed point of
//       execenv.sanitizeSkillName. These files are not embedded. --import
//       copies them onto the cloud runtime.
//
// PROVENANCE.md under the briefs tree is hand-written and untouched.
//
// Usage:
//   node scripts/aurora-import-experts.mjs --source /tmp/octop-expert-market
//   node scripts/aurora-import-experts.mjs --check
//   node scripts/aurora-import-experts.mjs --import /opt/aurora/experts
//
// --check recomputes both trees and byte-compares them with the committed
// ones; it never writes. Exit code 1 means drift.
// --import copies aurora-experts/ to <dest>, skipping _meta.json. It does
// not need the snapshot and does not rebuild the API.

import { existsSync } from "node:fs";
import { chmod, mkdir, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const BRIEFS_ROOT = "server/internal/aurora/experts";
const SKILLS_ROOT = "aurora-experts";
const LOCAL_INDEX_ROOT = "server/internal/aurora/experts-local";
const BRIEFS_ABS = join(REPO_ROOT, BRIEFS_ROOT);
const SKILLS_ABS = join(REPO_ROOT, SKILLS_ROOT);
const LOCAL_INDEX_ABS = join(REPO_ROOT, LOCAL_INDEX_ROOT, "index.json");
const WORKFLOW_SUFFIX = "/workflow.md";

// The six SkillHub scenes the 2026-10-14 sync installed. tech-test-automation
// is the pre-existing seventh template and is deliberately not imported.
export const SCENES = ["ecommerce", "content-creation", "marketing", "legal", "media", "design"];
export const EXCLUDED_SLUGS = ["tech-test-automation"];

const TICK = String.fromCharCode(96);
const NL = String.fromCharCode(10);
const CR = String.fromCharCode(13);

// code renders an inline Markdown code span without writing a backtick in
// this file, so the generator stays trivially diffable.
export function code(text) {
  return TICK + text + TICK;
}

export function fence(lang) {
  return TICK + TICK + TICK + lang;
}

function lines(...parts) {
  return parts.join(NL);
}

function die(message) {
  process.stderr.write("aurora-import-experts: " + message + NL);
  process.exit(2);
}

// readText reads one vendored file with line endings normalized to LF. The
// repository (gitattributes: * text=auto) stores LF and checks out LF, so a
// vendored file that kept its CRLF bytes would make --check fail on a fresh
// clone even though the snapshot never changed.
async function readText(full) {
  return (await readFile(full, "utf8")).replace(new RegExp(CR, "g"), "");
}

// parseFrontmatter splits a SKILL.md into its YAML block and its body. Line
// endings are normalized to LF so the vendored tree is byte-stable.
export function parseFrontmatter(text) {
  const normalized = text.replace(new RegExp(CR, "g"), "");
  if (!normalized.startsWith("---" + NL)) {
    return { frontmatter: "", body: normalized };
  }
  const end = normalized.indexOf(NL + "---", 3);
  if (end < 0) {
    return { frontmatter: "", body: normalized };
  }
  const frontmatter = normalized.slice(3 + NL.length, end);
  let bodyStart = end + NL.length + 3;
  // Drop the blank line(s) between the closing fence and the body.
  while (normalized.slice(bodyStart, bodyStart + NL.length) === NL) {
    bodyStart += NL.length;
  }
  return { frontmatter, body: normalized.slice(bodyStart) };
}

// frontmatterField reads one scalar field out of a frontmatter block.
export function frontmatterField(frontmatter, field) {
  const prefix = field + ":";
  for (const row of frontmatter.split(NL)) {
    if (row.startsWith(prefix)) {
      return row
        .slice(prefix.length)
        .trim()
        .replace(/^["']|["']$/g, "");
    }
  }
  return "";
}

// sceneOf resolves an expert scene. Some templates ship a scene in their
// skillset frontmatter and some do not; the slug prefix always carries it, so
// the prefix is the fallback and the frontmatter is the primary source.
export function sceneOf(slug, frontmatterScene) {
  if (SCENES.includes(frontmatterScene)) {
    return frontmatterScene;
  }
  const byLength = [...SCENES].sort((a, b) => b.length - a.length);
  for (const scene of byLength) {
    if (slug.startsWith(scene + "-")) {
      return scene;
    }
  }
  return "";
}

// namespacedSlug is the directory name of one sub-skill. It is a fixed point
// of execenv.sanitizeSkillName ([^a-z0-9]+ → one "-"), so the brief path and
// the directory the daemon copies are the same string. A single "-" is the
// separator this script adds; sub-skill slugs are unique within one expert.
export function namespacedSlug(expertSlug, subSlug) {
  return sanitizeSkillDir(expertSlug + "-" + subSlug);
}

// sanitizeSkillDir matches execenv.sanitizeSkillName. An empty result becomes
// "skill", which is also what a Chinese primary skill name collapses to, so
// the generator rejects that result for a sub-skill.
export function sanitizeSkillDir(name) {
  const cleaned = name
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return cleaned || "skill";
}

// rewriteSkillName points a sub-skill frontmatter name at its directory name
// so the name a model invokes and the directory on disk agree.
export function rewriteSkillName(text, name) {
  const parsed = parseFrontmatter(text);
  if (!parsed.frontmatter) {
    return "---" + NL + "name: " + name + NL + "---" + NL + NL + parsed.body;
  }
  let replaced = false;
  const rows = parsed.frontmatter.split(NL).map((row) => {
    if (!replaced && row.startsWith("name:")) {
      replaced = true;
      return "name: " + name;
    }
    return row;
  });
  if (!replaced) {
    rows.unshift("name: " + name);
  }
  return "---" + NL + rows.join(NL) + NL + "---" + NL + NL + parsed.body;
}

// isProvenancePath reports a SkillHub provenance file. It stays in the
// checkout so the snapshot can be audited, and --import does not copy it.
// Every other file, including dot directories and __init__.py, is skill content.
export function isProvenancePath(relPath) {
  return relPath.split("/").pop() === "_meta.json";
}

function sortedEntries(dir) {
  return readdir(dir, { withFileTypes: true }).then((entries) =>
    entries
      .filter((entry) => entry.isDirectory())
      .map((entry) => entry.name)
      .sort(),
  );
}

// isManagedBrief reports whether the generator owns one brief. Briefs for
// locally authored experts stay hand-written in the checkout, so --check must
// not compare them: a deliberate edit is not snapshot drift.
export function isManagedBrief(relPath, localSlugs) {
  if (!relPath.endsWith(WORKFLOW_SUFFIX)) {
    return false;
  }
  return !localSlugs.has(relPath.slice(0, -WORKFLOW_SUFFIX.length));
}

// readLocalIndex returns the hand-written expert entries. A missing file means
// a snapshot-only run, which is what the tests below exercise.
async function readLocalIndex(localIndexPath) {
  if (!localIndexPath || !existsSync(localIndexPath)) {
    return [];
  }
  const parsed = JSON.parse(await readFile(localIndexPath, "utf8"));
  return Array.isArray(parsed.experts) ? parsed.experts : [];
}

// localExpert converts one experts-local entry into the upstream expert shape
// so renderIndex carries both. A local expert has no vendored persona, no
// vendored workflow and no sub-skills: the brief is written by hand and is
// never generated.
function localExpert(entry) {
  const slug = entry.slug || "";
  if (!slug) {
    die("a locally authored expert has no slug");
  }
  return {
    slug,
    nameZh: entry.name_zh || slug,
    nameEn: entry.name_en || slug,
    scene: entry.scene || sceneOf(slug, ""),
    descriptionZh: entry.description_zh || "",
    descriptionEn: entry.description_en || "",
    persona: "",
    workflow: "",
    subskills: [],
    missingSubskills: entry.missing_subskills || [],
    quickPrompts: (entry.quick_prompts || []).map((prompt) => ({
      labelZh: prompt.label_zh || "",
      labelEn: prompt.label_en || "",
      promptZh: prompt.prompt_zh || "",
      promptEn: prompt.prompt_en || "",
    })),
    local: true,
  };
}

async function listFiles(dir, prefix) {
  const out = [];
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    const rel = prefix ? prefix + "/" + entry.name : entry.name;
    if (entry.isDirectory()) {
      out.push(...(await listFiles(full, rel)));
    } else if (entry.isFile()) {
      out.push({ path: rel, full });
    }
  }
  return out.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
}

// renderBrief wraps one expert into the Aurora brief contract: the five
// sections workflows_test.go enforces, plus the persona and the sub-skill list.
export function renderBrief(expert) {
  const bullets = expert.subskills
    .map((sub) => "- " + code(".claude/skills/" + sub.name + "/SKILL.md") + " — " + (sub.displayEn || sub.slug))
    .join(NL);
  const description = expert.descriptionEn || expert.descriptionZh;

  return lines(
    "# " + expert.nameEn,
    "",
    expert.nameZh + (description ? " — " + description : ""),
    "",
    "You run on a normal Multica agent: you have a shell. Do the work yourself.",
    "",
    "## Persona",
    "",
    expert.persona.trim(),
    "",
    "## Inputs",
    "",
    "- " + code("prompt") + ": the user instruction (required) — the goal, the",
    "  context, the materials and any constraints. It may also carry the source",
    "  material directly.",
    "- Zero or one attached document (" +
      [".txt", ".md", ".markdown", ".pdf", ".docx"].map(code).join(", ") +
      "). Fetch it with",
    "  " + code("multica attachment download <id>") + ", which writes it locally and prints",
    "  the path. It must be at most 25 MiB.",
    "- No provider credential is guaranteed. This run has a shell, the provider",
    "  CLIs and the Multica CLI on the ordinary agent surface, and nothing else:",
    "  any step below that needs an external account is best-effort.",
    "",
    "## Steps",
    "",
    "This expert ships " + expert.subskills.length + " supporting sub-skills, each with its own SKILL.md:",
    "",
    bullets,
    "",
    "Read a sub-skill SKILL.md before the workflow step that needs it.",
    "",
    "0. When a document is attached, read it into text first. DOC is the",
    "   downloaded file:",
    "",
    "   case \"$DOC\" in",
    "     *.txt|*.md|*.markdown) sed 's/^\\xEF\\xBB\\xBF//' \"$DOC\" > /tmp/source.txt ;;",
    "     *.pdf)  pdftotext \"$DOC\" - > /tmp/source.txt ;;",
    "     *.docx) unzip -p \"$DOC\" word/document.xml | sed -e 's|<w:br[^>]*>|\\n|g' -e 's|<w:tab[^>]*>|\\t|g' -e 's|</w:p>|\\n|g' -e 's/<[^>]*>//g' > /tmp/source.txt ;;",
    "     *) echo \"unsupported document format\" >&2; exit 1 ;;",
    "   esac",
    "",
    "   Stop and report it when the document is unreadable or its format is not",
    "   one of those.",
    "",
    "1. Then follow this expert workflow:",
    "",
    expert.workflow.trim(),
    "",
    "## Required outputs",
    "",
    "- " + code("<outputRoot>/artifacts/" + expert.slug + ".md") + " — the deliverable, UTF-8",
    "  Markdown, at most 2 MiB. It is the run only artifact, and it is the",
    "  primary one.",
    "",
    "## Artifact manifest",
    "",
    "Write " + code("<outputRoot>/.multica/aurora-artifacts.v1.json") + ". Run",
    code("mkdir -p \"<outputRoot>/.multica\"") + " first: nothing creates that directory",
    "for you. A local file artifact names its path relative to the output root;",
    "compute its size and hash from the file you wrote.",
    "",
    fence("json"),
    "{",
    '  "schema": "com.multica.aurora.artifacts",',
    '  "version": 1,',
    '  "task_id": "<the task id from your task context>",',
    '  "skill_id": "' + expert.slug + '",',
    '  "producer": { "id": "multica-aurora-runtime", "version": "1.0.0", "tree_sha256": null },',
    '  "provider_run": null,',
    '  "artifacts": [',
    "    {",
    '      "id": "primary-1",',
    '      "source": { "type": "file", "relative_path": "artifacts/' + expert.slug + '.md" },',
    '      "name": "' + expert.slug + '.md",',
    '      "kind": "text",',
    '      "role": "primary",',
    '      "format": "md",',
    '      "mime_type": "text/markdown",',
    '      "size_bytes": 0,',
    '      "sha256": "sha256:<64 hex>",',
    '      "metadata": {}',
    "    }",
    "  ]",
    "}",
    fence(""),
    "",
    "The daemon re-checks this manifest against the task and the skill: the",
    "producer id must be " + code("multica-aurora-runtime") + ", the skill id must be",
    code(expert.slug) + ", the primary artifact must be a " + code("text") + " output (this",
    "skill declares " + code("text") + " only), the file name extension must match its",
    "kind, and " + code("size_bytes") + " must be a non-negative integer. A missing",
    "manifest, a symlink or a non-regular file, more than 20 artifacts, or more",
    "than 600 MiB in total is rejected. Compute " + code("size_bytes") + " and",
    code("sha256") + " with " + code("stat -c %s") + " and " + code("sha256sum") + ".",
    "",
    "Use only " + code(".md") + ", " + code(".markdown") + " or " + code(".txt") + " for the artifact name:",
    "no other extension is accepted for a text artifact.",
    "",
    "## Failure behavior",
    "",
    "- Missing a required input: ask on the issue this run was claimed from —",
    "  " + code("multica issue comment add <issue id> --content-file ./question.md") + " —",
    '  then end this round. Declare it in the manifest: "outcome": "needs_input"',
    '  with "artifacts": []. Write no files and call no provider: anything you',
    "  store is counted as an artifact and billed.",
    "- The request carries no goal or material to work from: stop and report it.",
    "  Do not invent a subject.",
    "- A step below needs an external account, a browser session or a credential",
    "  this run does not have: do not fabricate its result. Produce the",
    "  deliverable from what the user supplied, state plainly which steps could",
    "  not be executed, and say what the user must run themselves.",
    "- Never claim completion unless the artifact file exists and the manifest",
    "  names it.",
    "",
  );
}

// renderIndex is the catalog inventory the Go side embeds. It names each
// sub-skill directory and does not carry file bodies: those live in
// aurora-experts/ and are imported onto the runtime. Key order is fixed and
// the file ends with a newline so --check is byte-stable.
function renderIndex(experts) {
  const data = {
    schema: 2,
    source: "octop expert_market snapshot (SkillHub skillsets)",
    experts: experts.map((expert) => ({
      slug: expert.slug,
      name_zh: expert.nameZh,
      name_en: expert.nameEn,
      scene: expert.scene,
      description_zh: expert.descriptionZh,
      description_en: expert.descriptionEn,
      missing_subskills: expert.missingSubskills,
      quick_prompts: expert.quickPrompts.map((prompt) => ({
        label_zh: prompt.labelZh,
        label_en: prompt.labelEn,
        prompt_zh: prompt.promptZh,
        prompt_en: prompt.promptEn,
      })),
      subskills: expert.subskills.map((sub) => ({
        slug: sub.slug,
        name: sub.name,
        display_zh: sub.displayZh,
        display_en: sub.displayEn,
      })),
    })),
  };
  return JSON.stringify(data, null, 2) + NL;
}

// buildLibrary turns a snapshot directory into the complete managed file set,
// keyed by repo-relative path. It is pure so --check and the tests can use it.
// Local experts are merged into the index only: their briefs and skill trees
// are not part of the managed set.
export async function buildLibrary(sourceDir, localIndexPath = LOCAL_INDEX_ABS) {
  const desired = new Map();
  const experts = [];

  for (const dirName of await sortedEntries(sourceDir)) {
    if (!dirName.startsWith("skillhub-skillset-")) {
      continue;
    }
    const slug = dirName.slice("skillhub-skillset-".length);
    if (EXCLUDED_SLUGS.includes(slug)) {
      continue;
    }
    const expertDir = join(sourceDir, dirName);
    const manifest = JSON.parse(await readFile(join(expertDir, "manifest.json"), "utf8"));
    const skillsRoot = join(expertDir, "skills");
    const skillsetSkill = join(skillsRoot, slug, "SKILL.md");
    if (!existsSync(skillsetSkill)) {
      die("template " + dirName + " has no skills/" + slug + "/SKILL.md");
    }
    const skillsetText = await readText(skillsetSkill);
    const skillset = parseFrontmatter(skillsetText);
    const scene = sceneOf(slug, manifest.scene || frontmatterField(skillset.frontmatter, "scene"));
    if (!scene) {
      continue;
    }
    const persona = await readText(join(expertDir, "SOUL.md"));

    // quick_prompts is the upstream "quick start" set: one card per workflow
    // step, each carrying a title and a fill-in-the-blanks prompt. The seeder
    // maps the first ones onto the agent's conversation starters.
    const quickPrompts = (manifest.quick_prompts || []).map((item) => ({
      labelZh: (item.title && item.title.zh) || "",
      labelEn: (item.title && item.title.en) || "",
      promptZh: (item.prompt && item.prompt.zh) || "",
      promptEn: (item.prompt && item.prompt.en) || "",
    }));

    const subskills = [];
    const missingSubskills = [];
    for (const subSlug of (await sortedEntries(skillsRoot)).filter((name) => name !== slug)) {
      const subDir = join(skillsRoot, subSlug);
      // An upstream download can fail and still leave an empty directory behind
      // (marketing-social-media-operation ships an empty content-writer). The
      // snapshot, not the skillset frontmatter, is the source of truth.
      if (!existsSync(join(subDir, "SKILL.md"))) {
        missingSubskills.push(subSlug);
        continue;
      }
      const name = namespacedSlug(slug, subSlug);
      if (name === "skill") {
        die(slug + "/" + subSlug + " sanitizes to the primary skill directory");
      }
      const content = rewriteSkillName(await readText(join(subDir, "SKILL.md")), name);
      const subFrontmatter = parseFrontmatter(content).frontmatter;
      const files = [];
      for (const file of await listFiles(subDir, "")) {
        const body = file.path === "SKILL.md" ? content : await readText(file.full);
        desired.set(SKILLS_ROOT + "/" + slug + "/" + name + "/" + file.path, body);
        if (isProvenancePath(file.path)) {
          continue;
        }
        files.push({ path: file.path, content: body });
      }
      subskills.push({
        slug: subSlug,
        name,
        displayZh: frontmatterField(subFrontmatter, "display_name") || subSlug,
        displayEn: frontmatterField(subFrontmatter, "display_name_en") || subSlug,
        files,
      });
    }

    const label = manifest.label || {};
    const description = manifest.description || {};
    const expert = {
      slug,
      nameZh: label.zh || slug,
      nameEn: label.en || slug,
      scene,
      descriptionZh: description.zh || "",
      descriptionEn: description.en || "",
      persona,
      workflow: skillset.body,
      subskills,
      missingSubskills,
      quickPrompts,
    };
    experts.push(expert);
    desired.set(BRIEFS_ROOT + "/" + slug + "/workflow.md", renderBrief(expert));
  }

  for (const entry of await readLocalIndex(localIndexPath)) {
    experts.push(localExpert(entry));
  }
  experts.sort((a, b) => (a.slug < b.slug ? -1 : a.slug > b.slug ? 1 : 0));

  const seen = new Set();
  const slugs = new Set();
  const dirs = new Set();
  for (const expert of experts) {
    if (seen.has(expert.nameZh)) {
      die("duplicate expert display name " + expert.nameZh);
    }
    seen.add(expert.nameZh);
    if (slugs.has(expert.slug)) {
      die("duplicate expert slug " + expert.slug);
    }
    slugs.add(expert.slug);
    for (const sub of expert.subskills) {
      const dir = expert.slug + "/" + sub.name;
      if (dirs.has(dir)) {
        die("duplicate skill directory " + dir);
      }
      dirs.add(dir);
    }
  }
  desired.set(BRIEFS_ROOT + "/index.json", renderIndex(experts));

  return { experts, files: desired };
}
// validateBriefs enforces the workflow contract at generation time so a bad
// upstream document cannot reach the repo silently.
export function validateBriefs(experts) {
  const problems = [];
  for (const expert of experts) {
    if (expert.quickPrompts.length === 0) {
      problems.push(expert.slug + " has no quick prompts to offer as agent starters");
    }
    if (
      expert.quickPrompts.length > 0 &&
      expert.quickPrompts.slice(0, 3).some((prompt) => !prompt.labelZh || !prompt.promptZh)
    ) {
      problems.push(expert.slug + " has an empty quick prompt label or prompt");
    }
    // A local expert's brief is hand-written, not rendered from a persona and
    // a workflow; its section contract is asserted by the Go workflow tests.
    if (expert.local) {
      continue;
    }
    const brief = renderBrief(expert);
    for (const sub of expert.subskills) {
      if (sub.name !== sanitizeSkillDir(sub.name) || sub.name.includes("--")) {
        problems.push(expert.slug + " sub-skill " + sub.slug + " is not a sanitizeSkillName fixed point");
      }
      const skillPath = ".claude/skills/" + sub.name + "/SKILL.md";
      if (!brief.includes(skillPath)) {
        problems.push(expert.slug + " brief does not name " + skillPath);
      }
    }
    for (const section of [
      "## Inputs",
      "## Steps",
      "## Required outputs",
      "## Artifact manifest",
      "## Failure behavior",
    ]) {
      if (!brief.includes(section)) {
        problems.push(expert.slug + " is missing " + section);
      }
    }
    if (!brief.includes("com.multica.aurora.artifacts")) {
      problems.push(expert.slug + " does not name the artifact manifest schema");
    }
    if (!brief.includes(expert.slug)) {
      problems.push(expert.slug + " does not name its own skill id");
    }
    for (const ref of brief.match(/aurora\.[a-z0-9_]+/g) || []) {
      if (ref !== "aurora.artifacts") {
        problems.push(expert.slug + " names the broker-shaped token " + ref);
      }
    }
  }
  return problems;
}

// importPlan is the file set --import copies onto the cloud runtime. It is
// every skill file except provenance _meta.json. Callers compare this list
// with the checkout so a reference cannot be vendored and then dropped.
export function importPlan(files) {
  const prefix = SKILLS_ROOT + "/";
  const plan = [];
  for (const [path, content] of files) {
    if (!path.startsWith(prefix)) {
      continue;
    }
    const rel = path.slice(prefix.length);
    if (isProvenancePath(rel)) {
      continue;
    }
    plan.push({ path: rel, content });
  }
  plan.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
  return plan;
}

async function currentFiles() {
  const out = new Map();
  const localSlugs = new Set((await readLocalIndex(LOCAL_INDEX_ABS)).map((entry) => entry.slug));
  if (existsSync(BRIEFS_ABS)) {
    for (const file of await listFiles(BRIEFS_ABS, "")) {
      const managed = file.path === "index.json" || isManagedBrief(file.path, localSlugs);
      if (managed) {
        out.set(BRIEFS_ROOT + "/" + file.path, await readText(file.full));
      }
    }
  }
  if (existsSync(SKILLS_ABS)) {
    for (const file of await listFiles(SKILLS_ABS, "")) {
      out.set(SKILLS_ROOT + "/" + file.path, await readText(file.full));
    }
  }
  return out;
}

function diff(desired, current) {
  const drift = [];
  for (const [path, content] of desired) {
    if (!current.has(path)) {
      drift.push("missing: " + path);
    } else if (current.get(path) !== content) {
      drift.push("changed: " + path);
    }
  }
  for (const path of current.keys()) {
    if (!desired.has(path)) {
      drift.push("orphan: " + path);
    }
  }
  return drift.sort();
}

// importExperts copies the committed skill tree to dest. _meta.json stays in
// the checkout. The destination layout is the layout the daemon reads:
// <dest>/<expert-slug>/<skill-dir>/... A shebang script is mode 0755: the
// skill documents run it as ./scripts/<name>, and a normal umask would
// otherwise clear the execute bit.
export async function importExperts(dest, sourceRoot = SKILLS_ABS) {
  if (!existsSync(sourceRoot)) {
    die("skill tree " + sourceRoot + " does not exist");
  }
  const copied = [];
  for (const file of await listFiles(sourceRoot, "")) {
    if (isProvenancePath(file.path)) {
      continue;
    }
    const body = await readFile(file.full);
    const full = join(dest, file.path);
    await mkdir(dirname(full), { recursive: true });
    await writeFile(full, body);
    await chmod(full, modeForImportedFile(body, await stat(file.full)));
    copied.push(file.path);
  }
  copied.sort();
  return copied;
}

function modeForImportedFile(body, info) {
  if (body.subarray(0, 2).toString("utf8") === "#!") {
    return 0o755;
  }
  const perm = info.mode & 0o777;
  return perm === 0 ? 0o644 : perm;
}

function parseArgs(argv) {
  const args = { check: false, source: "", importDest: "" };
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === "--check") {
      args.check = true;
    } else if (argv[i] === "--source") {
      args.source = argv[++i] || "";
    } else if (argv[i] === "--import") {
      args.importDest = argv[++i] || "";
    } else if (argv[i] === "--help" || argv[i] === "-h") {
      process.stdout.write(
        "usage: aurora-import-experts.mjs --source <snapshot-dir> | --check | --import <runtime-dir>" + NL,
      );
      process.exit(0);
    } else {
      die("unknown argument " + argv[i]);
    }
  }
  return args;
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.importDest) {
    const copied = await importExperts(args.importDest);
    process.stdout.write("imported " + copied.length + " skill files to " + args.importDest + NL);
    return;
  }
  if (!args.source && !args.check) {
    die("--source is required unless --check or --import is used");
  }
  if (args.check) {
    const source = args.source || process.env.OCTOP_EXPERT_MARKET || "/tmp/octop-expert-market";
    if (!existsSync(source)) {
      die("snapshot directory " + source + " does not exist; pass --source");
    }
    const { experts, files } = await buildLibrary(source);
    const problems = validateBriefs(experts);
    if (problems.length > 0) {
      die(problems.join(NL));
    }
    const drift = diff(files, await currentFiles());
    if (drift.length > 0) {
      process.stderr.write("aurora expert library is out of date:" + NL + drift.join(NL) + NL);
      process.exit(1);
    }
    process.stdout.write("aurora expert library is up to date (" + experts.length + " experts)" + NL);
    return;
  }

  const { experts, files } = await buildLibrary(args.source);
  const problems = validateBriefs(experts);
  if (problems.length > 0) {
    die(problems.join(NL));
  }
  for (const expert of experts) {
    // A local expert's brief is hand-written; regenerating the snapshot tree
    // must not delete it.
    if (expert.local) {
      continue;
    }
    await rm(join(BRIEFS_ABS, expert.slug), { recursive: true, force: true });
    await rm(join(SKILLS_ABS, expert.slug), { recursive: true, force: true });
  }
  for (const [path, content] of files) {
    const full = join(REPO_ROOT, path);
    await mkdir(dirname(full), { recursive: true });
    await writeFile(full, content, "utf8");
  }
  process.stdout.write(
    "wrote " +
      files.size +
      " files for " +
      experts.length +
      " experts (" +
      experts.reduce((sum, expert) => sum + expert.subskills.length, 0) +
      " sub-skills)" +
      NL,
  );
  for (const expert of experts) {
    if (expert.missingSubskills.length > 0) {
      process.stdout.write(
        "incomplete upstream package: " + expert.slug + " misses " + expert.missingSubskills.join(", ") + NL,
      );
    }
  }
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  main().catch((error) => {
    process.stderr.write(
      "aurora-import-experts: " + (error && error.stack ? error.stack : error) + NL,
    );
    process.exit(1);
  });
}
