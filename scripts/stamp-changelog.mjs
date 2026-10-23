#!/usr/bin/env node

//
// Moves the body of a changelog's `## [Unreleased]` section into a new
// `## [<version>] - <date>` section and leaves an empty `## [Unreleased]`
// above it. Modelled on agent-tools' release-changelog.js, but for one file
// and with an empty body written as "- No user-facing changes." instead of
// being skipped, so every release has a stamped section. Used by
// release-stamp.sh, which the release workflows run.
//
// Usage: node scripts/stamp-changelog.mjs <changelog-path> <version> [date]
//   date defaults to today (UTC), YYYY-MM-DD.
//
// Line endings are preserved: the file is handled line by line with each
// line's own terminator kept, and new lines use the file's first terminator.
//

import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";

const EMPTY_BODY_LINE = "- No user-facing changes.";
const UNRELEASED_HEADING = /^## \[Unreleased\]/;
const ANY_H2 = /^## /;

function escapeRegExp(text) {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

export function todayUtc() {
  return new Date().toISOString().slice(0, 10);
}

function isBlank(line) {
  return line.trim() === "";
}

// Returns the stamped text. Throws if there is no `## [Unreleased]` heading
// or `## [<version>]` already has a heading (re-stamping would duplicate it).
export function stampChangelog(text, version, date) {
  const lines = text.split(/(?<=\n)/);
  const eol = text.includes("\r\n") ? "\r\n" : "\n";

  const start = lines.findIndex(line => UNRELEASED_HEADING.test(line));
  if (start === -1) {
    throw new Error('no "## [Unreleased]" heading found');
  }

  const versionHeading = new RegExp(`^## \\[${escapeRegExp(version)}\\]`);
  if (lines.some(line => versionHeading.test(line))) {
    throw new Error(`"## [${version}]" already exists; refusing to duplicate it`);
  }

  let end = lines.findIndex((line, index) => index > start && ANY_H2.test(line));
  if (end === -1) {
    end = lines.length;
  }

  const body = lines.slice(start + 1, end);
  while (body.length > 0 && isBlank(body[0])) {
    body.shift();
  }
  // Blank lines after the last entry stay where they were at the end of the
  // file; before a following heading one blank line is written instead.
  const trailingBlank = [];
  while (body.length > 0 && isBlank(body[body.length - 1])) {
    trailingBlank.unshift(body.pop());
  }
  // The last body line may lack a terminator when the file ends without one.
  const bodyLines = body.length === 0
    ? [EMPTY_BODY_LINE + eol]
    : body.map(line => (line.endsWith("\n") ? line : line + eol));

  const before = lines.slice(0, start);
  const heading = lines[start].endsWith("\n") ? lines[start] : lines[start] + eol;
  const after = lines.slice(end);

  const out = [
    ...before,
    heading,
    eol,
    `## [${version}] - ${date}${eol}`,
    eol,
    ...bodyLines,
  ];
  out.push(...(after.length > 0 ? [eol, ...after] : trailingBlank));
  return out.join("");
}

function main() {
  const [file, version, date = todayUtc()] = process.argv.slice(2);

  if (!file || !version || !/^[0-9A-Za-z][0-9A-Za-z.+-]*$/.test(version) || !/^\d{4}-\d{2}-\d{2}$/.test(date)) {
    console.error("Usage: node scripts/stamp-changelog.mjs <changelog-path> <version> [YYYY-MM-DD]  (e.g. assistant/CHANGELOG.md 2026.10.4)");
    process.exit(2);
  }

  let text;
  try {
    text = fs.readFileSync(file, "utf8");
  } catch (err) {
    console.error(`${file}: cannot read: ${err.message}`);
    process.exit(1);
  }

  let stamped;
  try {
    stamped = stampChangelog(text, version, date);
  } catch (err) {
    console.error(`${file}: ${err.message}`);
    process.exit(1);
  }

  fs.writeFileSync(file, stamped, "utf8");
  console.log(`${file} -> [${version}] - ${date}`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  main();
}
