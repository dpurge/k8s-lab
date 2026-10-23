import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { stampChangelog } from "./stamp-changelog.mjs";

const script = path.join(path.dirname(fileURLToPath(import.meta.url)), "stamp-changelog.mjs");
const HEADER = "# Changelog\n\nAll notable changes.\n\n";

test("moves the Unreleased body into a new version section", () => {
  const input = `${HEADER}## [Unreleased]\n\n- Added a thing.\n- Fixed a bug.\n\n## [2026.9.0] - 2026-09-01\n\n- Old.\n`;
  const expected = `${HEADER}## [Unreleased]\n\n## [2026.10.4] - 2026-10-23\n\n- Added a thing.\n- Fixed a bug.\n\n## [2026.9.0] - 2026-09-01\n\n- Old.\n`;
  assert.equal(stampChangelog(input, "2026.10.4", "2026-10-23"), expected);
});

test("an empty Unreleased body gets the no-changes line", () => {
  const input = `${HEADER}## [Unreleased]\n\n`;
  const expected = `${HEADER}## [Unreleased]\n\n## [1.0.0] - 2026-10-23\n\n- No user-facing changes.\n`;
  assert.equal(stampChangelog(input, "1.0.0", "2026-10-23"), expected);
});

test("a file ending right after the Unreleased entries (no newline) still stamps", () => {
  const out = stampChangelog("## [Unreleased]\n\n- One.", "1.0.0", "2026-10-23");
  assert.equal(out, "## [Unreleased]\n\n## [1.0.0] - 2026-10-23\n\n- One.\n");
});

test("trailing blank lines at the end of the file are kept", () => {
  const out = stampChangelog("## [Unreleased]\n\n- One.\n\n", "1.0.0", "2026-10-23");
  assert.equal(out, "## [Unreleased]\n\n## [1.0.0] - 2026-10-23\n\n- One.\n\n");
});

test("categories are preserved verbatim", () => {
  const body = "### Added\n\n- A.\n\n### Fixed\n\n- B.\n";
  const input = `${HEADER}## [Unreleased]\n\n${body}\n## [0.1.0] - 2026-01-01\n\n- Old.\n`;
  const out = stampChangelog(input, "0.2.0", "2026-10-23");
  assert.ok(out.includes(`## [0.2.0] - 2026-10-23\n\n${body}\n## [0.1.0]`));
  assert.ok(out.includes("## [Unreleased]\n\n## [0.2.0]"));
});

test("an existing version is refused", () => {
  const input = `${HEADER}## [Unreleased]\n\n- X.\n\n## [1.0.0] - 2026-01-01\n\n- Old.\n`;
  assert.throws(() => stampChangelog(input, "1.0.0", "2026-10-23"), /already exists/);
});

test("a missing Unreleased heading is refused", () => {
  assert.throws(() => stampChangelog(`${HEADER}## [1.0.0] - 2026-01-01\n`, "1.1.0", "2026-10-23"), /no "## \[Unreleased\]" heading/);
});

test("CRLF line endings are preserved and no bare LF appears", () => {
  const input = `${HEADER}## [Unreleased]\n\n- Added.\n\n## [0.1.0] - 2026-01-01\n\n- Old.\n`.replace(/\n/g, "\r\n");
  const out = stampChangelog(input, "0.2.0", "2026-10-23");
  assert.equal(out.replace(/\r\n/g, ""), out.replace(/\r\n/g, "").replace(/\n/g, ""), "bare LF found");
  assert.equal(
    out,
    `${HEADER}## [Unreleased]\n\n## [0.2.0] - 2026-10-23\n\n- Added.\n\n## [0.1.0] - 2026-01-01\n\n- Old.\n`.replace(/\n/g, "\r\n"),
  );
});

test("CLI: second run on the same version fails and leaves the file unchanged", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "stamp-changelog-"));
  try {
    const file = path.join(dir, "CHANGELOG.md");
    fs.writeFileSync(file, `${HEADER}## [Unreleased]\n\n- Added.\n`);

    const first = spawnSync("node", [script, file, "2026.10.4", "2026-10-23"], { encoding: "utf8" });
    assert.equal(first.status, 0, first.stderr);
    const stamped = fs.readFileSync(file, "utf8");
    assert.ok(stamped.includes("## [2026.10.4] - 2026-10-23\n\n- Added.\n"));

    const second = spawnSync("node", [script, file, "2026.10.4", "2026-10-23"], { encoding: "utf8" });
    assert.notEqual(second.status, 0);
    assert.match(second.stderr, /already exists/);
    assert.equal(fs.readFileSync(file, "utf8"), stamped);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("CLI: usage error and default date", () => {
  const bad = spawnSync("node", [script], { encoding: "utf8" });
  assert.notEqual(bad.status, 0);
  assert.match(bad.stderr, /Usage/);

  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "stamp-changelog-"));
  try {
    const file = path.join(dir, "CHANGELOG.md");
    fs.writeFileSync(file, "## [Unreleased]\n");
    const run = spawnSync("node", [script, file, "1.0.0"], { encoding: "utf8" });
    assert.equal(run.status, 0, run.stderr);
    const today = new Date().toISOString().slice(0, 10);
    assert.ok(fs.readFileSync(file, "utf8").includes(`## [1.0.0] - ${today}`));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
