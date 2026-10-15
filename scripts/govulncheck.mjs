#!/usr/bin/env node
// govulncheck fails on any vulnerability that reaches this code, but some
// advisories have no released fix. This wrapper keeps the gate meaningful: it
// runs the same scan, then fails unless every *affecting* finding is named in
// govulncheck-allowlist.json with a reason and a revisit condition. Findings
// that do not reach our code never need an entry, and a listed vulnerability
// that moves to a different module fails closed instead of staying excused.
import { readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const defaultAllowlistPath = join(here, "govulncheck-allowlist.json");

/**
 * Split a stream of concatenated JSON objects. govulncheck's -format json
 * pretty-prints each object and its legacy -json writes one per line; the
 * bracket/string scan accepts both instead of assuming a line layout.
 */
export function splitJsonStream(text) {
  const objects = [];
  let depth = 0;
  let start = -1;
  let inString = false;
  let escaped = false;
  for (let i = 0; i < text.length; i += 1) {
    const ch = text[i];
    if (inString) {
      if (escaped) escaped = false;
      else if (ch === "\\") escaped = true;
      else if (ch === '"') inString = false;
      continue;
    }
    if (ch === '"') {
      inString = true;
    } else if (ch === "{") {
      if (depth === 0) start = i;
      depth += 1;
    } else if (ch === "}") {
      depth -= 1;
      if (depth < 0) throw new Error("govulncheck emitted an unbalanced JSON stream");
      if (depth === 0 && start >= 0) {
        objects.push(text.slice(start, i + 1));
        start = -1;
      }
    }
  }
  if (inString || depth !== 0) throw new Error("govulncheck emitted a truncated JSON stream");
  return objects;
}

/** Parse govulncheck's JSON stream and keep the findings that reach our code. */
export function parseGovulncheckJson(text) {
  const findings = new Map();
  for (const object of splitJsonStream(text)) {
    let message;
    try {
      message = JSON.parse(object);
    } catch {
      throw new Error(`govulncheck emitted a non-JSON object: ${object.slice(0, 160)}`);
    }
    const finding = message.finding;
    // Progress, config and OSV frames have no finding or no trace.
    if (!finding || !Array.isArray(finding.trace) || finding.trace.length === 0) continue;
    // govulncheck reports three levels: the module that requires the
    // vulnerability, the package we import, and the symbol we call. Only the
    // symbol level affects this code, and it is the set the text report counts
    // as "your code is affected"; the other two would make the gate demand
    // entries for code paths we never reach.
    if (!finding.trace.some((frame) => typeof frame.function === "string" && frame.function !== "")) {
      continue;
    }
    if (typeof finding.osv !== "string" || finding.osv === "") {
      throw new Error("govulncheck reported a finding without an OSV id");
    }
    const entry = findings.get(finding.osv) ?? {
      id: finding.osv,
      module: finding.trace[0]?.module ?? "",
      fixedVersion: finding.fixed_version ?? "",
      traces: 0,
    };
    entry.traces += 1;
    findings.set(finding.osv, entry);
  }
  return [...findings.values()].sort((a, b) => a.id.localeCompare(b.id));
}

/** Split affecting findings into acknowledged and blocked ones. */
export function evaluateFindings(findings, allowlist) {
  const allowed = new Map((allowlist.allowed ?? []).map((entry) => [entry.id, entry]));
  const blocked = [];
  const acknowledged = [];
  for (const finding of findings) {
    const entry = allowed.get(finding.id);
    // A module mismatch means the allowance is stale: fail closed.
    if (entry && entry.module === finding.module) {
      acknowledged.push({ finding, entry });
    } else {
      blocked.push(finding);
    }
  }
  return { blocked, acknowledged };
}

export function readAllowlist(path = defaultAllowlistPath) {
  const allowlist = JSON.parse(readFileSync(path, "utf8"));
  if (!Array.isArray(allowlist.allowed)) {
    throw new Error(`${path}: expected an "allowed" array`);
  }
  for (const entry of allowlist.allowed) {
    for (const field of ["id", "module", "reason", "revisit"]) {
      if (typeof entry[field] !== "string" || entry[field] === "") {
        throw new Error(`${path}: entry ${entry.id ?? "(no id)"} needs a non-empty "${field}"`);
      }
    }
  }
  return allowlist;
}

function main() {
  const allowlistFlag = process.argv.indexOf("--allowlist");
  const allowlistPath = allowlistFlag === -1 ? defaultAllowlistPath : process.argv[allowlistFlag + 1];
  const allowlist = readAllowlist(allowlistPath);

  const scan = spawnSync("go", ["tool", "govulncheck", "-json", "./..."], {
    cwd: join(here, "..", "server"),
    encoding: "utf8",
    maxBuffer: 128 * 1024 * 1024,
  });
  // govulncheck exits 3 when it finds vulnerabilities; anything else is a tool failure.
  if (scan.error) throw scan.error;
  if (scan.status !== 0 && scan.status !== 3) {
    process.stderr.write(scan.stderr ?? "");
    throw new Error(`govulncheck failed with exit code ${scan.status}`);
  }

  const findings = parseGovulncheckJson(scan.stdout ?? "");
  const { blocked, acknowledged } = evaluateFindings(findings, allowlist);

  if (acknowledged.length > 0) {
    console.log(`govulncheck: ${acknowledged.length} affecting vulnerability(ies), all acknowledged:`);
    for (const { finding, entry } of acknowledged) {
      console.log(`  ${finding.id}  ${finding.module}  (${finding.traces} trace(s), fixed: ${finding.fixedVersion || "not released"})`);
      console.log(`      reason: ${entry.reason}`);
    }
  }
  if (blocked.length > 0) {
    console.error(`govulncheck: ${blocked.length} unacknowledged vulnerability(ies)`);
    for (const finding of blocked) {
      console.error(`  ${finding.id}  ${finding.module}  (${finding.traces} trace(s), fixed: ${finding.fixedVersion || "not released"})`);
    }
    console.error("Fix the dependency, or add a reasoned entry to scripts/govulncheck-allowlist.json.");
    process.exitCode = 1;
    return;
  }
  if (findings.length === 0) {
    console.log("govulncheck: no vulnerabilities affect this code");
  }
}

if (process.argv[1] && import.meta.url === `file://${process.argv[1]}`) {
  main();
}
