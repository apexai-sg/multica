import assert from "node:assert/strict";
import { test } from "node:test";

import { evaluateFindings, parseGovulncheckJson, splitJsonStream } from "./govulncheck.mjs";

const finding = (osv, module) =>
  JSON.stringify({ finding: { osv, fixed_version: "", trace: [{ module, package: module, function: "init" }] } });

test("parseGovulncheckJson keeps only findings that reach our code", () => {
  const output = [
    JSON.stringify({ config: { protocol_version: "v1.0.0" } }),
    JSON.stringify({ progress: { message: "Scanning..." } }),
    JSON.stringify({ osv: { id: "GO-2026-0001" } }),
    // Module- and package-level findings do not affect this code.
    JSON.stringify({ finding: { osv: "GO-2026-9999", trace: [{ module: "github.com/docker/docker", version: "v28.5.1+incompatible" }] } }),
    JSON.stringify({ finding: { osv: "GO-2026-9998", trace: [{ module: "github.com/docker/docker", package: "github.com/docker/docker/api/types" }] } }),
    finding("GO-2026-4887", "github.com/docker/docker"),
    finding("GO-2026-4887", "github.com/docker/docker"),
    finding("GO-2026-4883", "github.com/docker/docker"),
  ].join("\n");
  assert.deepEqual(parseGovulncheckJson(output), [
    { id: "GO-2026-4883", module: "github.com/docker/docker", fixedVersion: "", traces: 1 },
    { id: "GO-2026-4887", module: "github.com/docker/docker", fixedVersion: "", traces: 2 },
  ]);
});

test("splitJsonStream frames both pretty-printed and line-delimited output", () => {
  const pretty = JSON.stringify({ a: 1 }, null, 2) + "\n" + JSON.stringify({ b: [2, 3] }, null, 2);
  assert.deepEqual(splitJsonStream(pretty).map((o) => JSON.parse(o)), [{ a: 1 }, { b: [2, 3] }]);
  const jsonl = '{"a":1}\n{"b":{"c":"}"}}\n';
  assert.deepEqual(splitJsonStream(jsonl).map((o) => JSON.parse(o)), [{ a: 1 }, { b: { c: "}" } }]);
  assert.throws(() => splitJsonStream('{"a":1'), /truncated JSON stream/);
  assert.throws(() => splitJsonStream('"unterminated'), /truncated JSON stream/);
});

test("parseGovulncheckJson fails closed on malformed streams", () => {
  assert.throws(() => parseGovulncheckJson('{"unterminated": '), /truncated JSON stream/);
  assert.throws(
    () => parseGovulncheckJson(JSON.stringify({ finding: { trace: [{ module: "m", function: "init" }] } })),
    /without an OSV id/,
  );
});

test("evaluateFindings acknowledges only reasoned entries for the same module", () => {
  const allowlist = {
    allowed: [{ id: "GO-2026-4887", module: "github.com/docker/docker", reason: "r", revisit: "v" }],
  };
  const { acknowledged, blocked } = evaluateFindings(
    [
      { id: "GO-2026-4887", module: "github.com/docker/docker", traces: 3 },
      { id: "GO-2026-4883", module: "github.com/docker/docker", traces: 1 },
      { id: "GO-2026-4887", module: "github.com/other/module", traces: 1 },
    ],
    allowlist,
  );
  assert.deepEqual(acknowledged.map(({ finding }) => [finding.id, finding.module]), [
    ["GO-2026-4887", "github.com/docker/docker"],
  ]);
  assert.deepEqual(blocked.map(({ id }) => id), ["GO-2026-4883", "GO-2026-4887"]);
});

test("evaluateFindings blocks everything when nothing is allowed", () => {
  const { acknowledged, blocked } = evaluateFindings([{ id: "GO-2026-4887", module: "m", traces: 1 }], {});
  assert.equal(acknowledged.length, 0);
  assert.equal(blocked.length, 1);
});
