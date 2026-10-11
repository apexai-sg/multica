// @vitest-environment node
import { readdirSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { describe, expect, it } from "vitest";
import ts from "typescript";

/**
 * The Aurora design layer is two files that must agree: the stylesheet that
 * declares every `aurora-*` class, and the app's globals.css that imports it and
 * bridges the Multica tokens onto its palette. Neither can fail loudly on its
 * own — a mistyped class name or an undeclared variable renders nothing at
 * all — so this test is the only thing between a typo and a silently unstyled
 * control.
 */

const repoRoot = resolve(import.meta.dirname, "../../..");
const css = readFileSync(
  resolve(repoRoot, "packages/views/aurora/aurora.css"),
  "utf8",
);
const globals = readFileSync(
  resolve(repoRoot, "apps/aurora/app/globals.css"),
  "utf8",
);

/** Every class selector the stylesheet defines, without the leading dot. */
function declaredClasses(source: string): Set<string> {
  const names = new Set<string>();
  for (const match of source.matchAll(/\.(aurora-[a-z0-9-]+)/g)) {
    names.add(match[1]!);
  }
  return names;
}

/** Every `--aurora-*` variable the stylesheet reads. */
function referencedVariables(source: string): Set<string> {
  const names = new Set<string>();
  for (const match of source.matchAll(/var\((--aurora-[a-z0-9-]+)/g)) {
    names.add(match[1]!);
  }
  return names;
}

/** Every `--aurora-*` variable the stylesheet declares. */
function declaredVariables(source: string): Set<string> {
  const names = new Set<string>();
  for (const match of source.matchAll(/^\s*(--aurora-[a-z0-9-]+)\s*:/gm)) {
    names.add(match[1]!);
  }
  return names;
}

/** Explicit finite families used by the three concatenated class expressions. */
const DYNAMIC_CLASSES: Record<string, string[]> = {
  "aurora-avatar--": [
    "coral",
    "violet",
    "rose",
    "mint",
    "amber",
    "blue",
    "rust",
    "strawberry",
  ],
  "aurora-orbit-avatar--": ["1", "2", "3", "4"],
  "aurora-tint-": ["1", "2", "3", "4", "5", "6"],
};
const CLASS_CONSTANTS = new Set([
  "GRID_CLASS",
  "TONE_CLASS",
  "AURORA_RUNTIME_VISUAL",
  "GENERATION_STATUS_PILL",
]);

/** Inspect JSX className expressions and the named class maps, never IDs/imports. */
function classesInSource(source: string): Set<string> {
  const file = ts.createSourceFile(
    "source.tsx",
    source,
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TSX,
  );
  const used = new Set<string>();
  const collect = (node: ts.Node) => {
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
      for (const token of node.text.split(/\s+/)) {
        if (!token.startsWith("aurora-")) continue;
        const suffixes = DYNAMIC_CLASSES[token];
        if (suffixes) suffixes.forEach((suffix) => used.add(token + suffix));
        else used.add(token);
      }
    }
    ts.forEachChild(node, collect);
  };
  const visit = (node: ts.Node) => {
    if (
      ts.isJsxAttribute(node) &&
      node.name.getText(file) === "className" &&
      node.initializer
    ) {
      collect(node.initializer);
    } else if (
      ts.isVariableDeclaration(node) &&
      ts.isIdentifier(node.name) &&
      CLASS_CONSTANTS.has(node.name.text) &&
      node.initializer
    ) {
      collect(node.initializer);
    }
    ts.forEachChild(node, visit);
  };
  visit(file);
  return used;
}

/** All non-test TS/TSX sources under the app and its shared Aurora views. */
function classesUsedIn(root: string): Set<string> {
  const used = new Set<string>();
  const walk = (current: string) => {
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      if (entry.name === "node_modules" || entry.name === ".next") continue;
      const next = join(current, entry.name);
      if (entry.isDirectory()) walk(next);
      else if (
        /\.tsx?$/.test(entry.name) &&
        !/\.(test|spec)\.tsx?$/.test(entry.name)
      ) {
        classesInSource(readFileSync(next, "utf8")).forEach((name) =>
          used.add(name),
        );
      }
    }
  };
  walk(root);
  return used;
}

const declared = declaredClasses(css);

describe("aurora design layer", () => {
  it("pins the light palette and Geist on the root layout", () => {
    const layout = readFileSync(
      resolve(repoRoot, "apps/aurora/app/layout.tsx"),
      "utf8",
    );
    expect(layout).toContain('forcedTheme="light"');
    expect(layout).toContain('className="aurora-theme h-full overflow-hidden"');
    expect(layout).toContain('variable: "--font-geist-sans"');
    expect(layout).toContain("geistSans.variable");
    expect(globals).toContain("var(--font-geist-sans)");
  });

  it("keeps shared controls inside Aurora at the design target sizes", () => {
    for (const slot of ["button", "sheet-close", "dialog-close"]) {
      expect(css).toContain(`.aurora-theme [data-slot="${slot}"]`);
    }
    const controls = css.match(
      /\.aurora-theme \[data-slot="button"\][^{]*\{([^}]+)\}/,
    )?.[1];
    expect(controls).toContain("min-width: 40px;");
    expect(controls).toContain("min-height: 40px;");
    expect(css).toMatch(
      /\.aurora-theme \[data-slot="input"\][^{]*\{\s*min-height: 44px;/,
    );
  });
  it("sizes the actual grouped input and leaves room inside its bordered group", () => {
    expect(css).toMatch(/\.aurora-theme input\[data-slot="input-group-control"\][^{]*\{\s*min-height: 44px;/);
    expect(css).toMatch(/\.aurora-theme \[data-slot="input-group"\]:has\(> input\[data-slot="input-group-control"\]\)\s*\{[^}]*height: auto;/);
  });

  it("imports the stylesheet from the app's globals", () => {
    expect(globals).toContain("packages/views/aurora/aurora.css");
  });

  it("declares every --aurora variable it reads", () => {
    const variables = declaredVariables(css);
    const missing = [...referencedVariables(css)].filter(
      (name) => !variables.has(name),
    );
    expect(missing).toEqual([]);
  });

  it("defines every aurora class the components use", () => {
    const used = new Set([
      ...classesUsedIn(resolve(repoRoot, "packages/views/aurora")),
      ...classesUsedIn(resolve(repoRoot, "apps/aurora")),
    ]);
    const unresolved = [...used].filter((token) => !declared.has(token));
    expect(unresolved).toEqual([]);
  });

  it("ignores module paths and DOM IDs while checking actual class names", () => {
    const source = `
      import { Providers } from "./aurora-providers";
      const GRID_CLASS = "aurora-card-grid";
      const AURORA_RUNTIME_VISUAL = { online: { dot: "aurora-dot" } };
      const view = <div id="aurora-prompt" className={cn("aurora-page", GRID_CLASS)} />;
      const typo = <span className="aurora-page-typo" />;
      const dynamic = <span className={"aurora-tint-" + index} />;
    `;
    const used = classesInSource(source);
    expect(used.has("aurora-providers")).toBe(false);
    expect(used.has("aurora-prompt")).toBe(false);
    expect(used.has("aurora-card-grid")).toBe(true);
    expect(used.has("aurora-dot")).toBe(true);
    expect(used.has("aurora-tint-6")).toBe(true);
    expect([...used].filter((name) => !declared.has(name))).toEqual([
      "aurora-page-typo",
    ]);
  });

  it("bridges the palette onto the semantic tokens the primitives read", () => {
    for (const token of [
      "--background",
      "--foreground",
      "--card",
      "--popover",
      "--primary",
      "--primary-foreground",
      "--secondary",
      "--muted",
      "--muted-foreground",
      "--accent",
      "--destructive",
      "--border",
      "--input",
      "--ring",
      "--sidebar",
      "--sidebar-accent",
      "--sidebar-accent-foreground",
      "--surface-border",
    ]) {
      expect(globals).toContain(token + ":");
    }
  });

  it("keeps body copy at or above DESIGN.md's floor", () => {
    expect(globals).toContain("--text-caption: 13px");
    expect(globals).toContain("--text-micro: 12px");
    expect(globals).toContain("--text-body-lg: 16px");
  });
});
