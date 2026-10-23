import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { checkCi } from "./check-ci.mjs";

const scriptsDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.join(scriptsDir, "..");

// Copies only what check-ci reads from the real repo into a temp tree.
function copyRepoState() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "check-ci-"));
  const copy = relative => {
    fs.mkdirSync(path.dirname(path.join(dir, relative)), { recursive: true });
    fs.copyFileSync(path.join(repoRoot, relative), path.join(dir, relative));
  };
  copy("scripts/ci-test.sh");
  for (const name of fs.readdirSync(path.join(repoRoot, ".github", "workflows"))) {
    if (/^release-.+\.yml$/.test(name)) {
      copy(`.github/workflows/${name}`);
      const app = name.replace(/^release-/, "").replace(/\.yml$/, "");
      for (const file of ["go.mod", "Dockerfile", "CHANGELOG.md"]) {
        copy(`${app}/${file}`);
      }
    }
  }
  return dir;
}

function edit(dir, relative, change) {
  const file = path.join(dir, relative);
  const before = fs.readFileSync(file, "utf8");
  const after = change(before);
  assert.notEqual(after, before, `edit of ${relative} changed nothing`);
  fs.writeFileSync(file, after);
}

function withCopy(run) {
  const dir = copyRepoState();
  try {
    run(dir);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

test("the repo's own state passes", () => {
  assert.deepEqual(checkCi(repoRoot), []);
});

test("knowledge without shared/** in paths fails (CLI exit code and message)", () => {
  withCopy(dir => {
    edit(dir, ".github/workflows/release-knowledge.yml", text => text.replace("      - 'shared/**'\n", ""));
    const run = spawnSync("node", [path.join(scriptsDir, "check-ci.mjs"), dir], { encoding: "utf8" });
    assert.equal(run.status, 1);
    assert.match(run.stderr, /release-knowledge\.yml: knowledge\/go\.mod replaces shared/);
    assert.equal(checkCi(dir).length, 1);
  });
});

test("dictionary does not need shared/**", () => {
  withCopy(dir => {
    assert.ok(!fs.readFileSync(path.join(dir, ".github/workflows/release-dictionary.yml"), "utf8").includes("'shared/**'"));
    assert.deepEqual(checkCi(dir), []);
  });
});

test("a workflow that does not call the scripts is reported for each", () => {
  withCopy(dir => {
    edit(dir, ".github/workflows/release-assistant.yml", text => text.replaceAll("scripts/ci-test.sh", "x").replaceAll("scripts/release-stamp.sh", "y"));
    const problems = checkCi(dir);
    assert.ok(problems.some(p => p.includes("does not call scripts/ci-test.sh")));
    assert.ok(problems.some(p => p.includes("does not call scripts/release-stamp.sh")));
  });
});

test("a missing Unreleased heading is reported", () => {
  withCopy(dir => {
    edit(dir, "phraseforge/CHANGELOG.md", text => text.replace("## [Unreleased]", "## [Next]"));
    assert.ok(checkCi(dir).some(p => p.startsWith("phraseforge/CHANGELOG.md")));
  });
});

test("a Node stage whose ci-test case lacks the shared/ui build is reported", () => {
  withCopy(dir => {
    edit(dir, "scripts/ci-test.sh", text => text.replaceAll("shared/ui", "elsewhere"));
    const problems = checkCi(dir);
    assert.ok(problems.some(p => p.includes("assistant/Dockerfile has a Node (ui) stage")));
    assert.ok(!problems.some(p => p.includes("dictionary")));
  });
});
