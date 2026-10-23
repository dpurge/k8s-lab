#!/usr/bin/env node

//
// Checks that the release workflows, scripts/ci-test.sh and the apps agree, so a
// CI gap is caught before a push instead of by a failed or missing release.
// Reports every problem, not just the first. Run by `task check-ci`.
//
// Usage: node scripts/check-ci.mjs [repo-root]
//
// An "app" is every .github/workflows/release-<app>.yml. Checks, per app:
//   1. go.mod has a `replace ... shared` line  =>  the workflow's `paths:` lists
//      'shared/**' (otherwise a change to shared/ never releases the app).
//   2. the workflow calls scripts/ci-test.sh and scripts/release-stamp.sh.
//   3. <app>/CHANGELOG.md has a `## [Unreleased]` heading (what the stamp moves).
//   4. the Dockerfile has a Node stage (`FROM node...`)  =>  the app's case arm
//      in scripts/ci-test.sh builds shared/ui.
//
// Limitation: this is line parsing, not YAML or shell parsing. It expects the
// repo's current layout: `paths:` entries as `- '<glob>'` lines under
// `paths:`, and ci-test.sh arms written as `a | b | c)` ending in `;;`.
//

import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath, pathToFileURL } from "node:url";

function readLines(file) {
  return fs.readFileSync(file, "utf8").split(/\r?\n/);
}

// The glob entries of the first `paths:` list in a workflow file.
function workflowPaths(lines) {
  const start = lines.findIndex(line => /^\s*paths:\s*$/.test(line));
  if (start === -1) {
    return [];
  }
  const globs = [];
  for (const line of lines.slice(start + 1)) {
    const entry = line.match(/^\s*-\s*['"]?([^'"\s]+)['"]?\s*$/);
    if (!entry) {
      break;
    }
    globs.push(entry[1]);
  }
  return globs;
}

// The lines of the `case` arm of ci-test.sh that names the app, or null.
function ciTestArm(lines, app) {
  const armStart = lines.findIndex(line => {
    const arm = line.match(/^\s*([a-z][a-z0-9-]*(?:\s*\|\s*[a-z][a-z0-9-]*)*)\s*\)/);
    return arm !== null && arm[1].split("|").map(name => name.trim()).includes(app);
  });
  if (armStart === -1) {
    return null;
  }
  const arm = [];
  for (const line of lines.slice(armStart)) {
    arm.push(line);
    if (/;;\s*$/.test(line)) {
      break;
    }
  }
  return arm;
}

export function checkCi(root) {
  const problems = [];
  const workflowDir = path.join(root, ".github", "workflows");
  const apps = fs.existsSync(workflowDir)
    ? fs.readdirSync(workflowDir)
        .map(name => name.match(/^release-(.+)\.yml$/))
        .filter(Boolean)
        .map(match => match[1])
        .sort()
    : [];
  if (apps.length === 0) {
    problems.push(`no .github/workflows/release-<app>.yml found under ${root}`);
  }

  const ciTestFile = path.join(root, "scripts", "ci-test.sh");
  const ciTest = fs.existsSync(ciTestFile) ? readLines(ciTestFile) : null;
  if (ciTest === null) {
    problems.push("scripts/ci-test.sh is missing");
  }

  for (const app of apps) {
    const workflowName = `.github/workflows/release-${app}.yml`;
    const workflow = readLines(path.join(workflowDir, `release-${app}.yml`));
    const globs = workflowPaths(workflow);

    const goMod = path.join(root, app, "go.mod");
    if (fs.existsSync(goMod) && readLines(goMod).some(line => /^replace\s.*\bshared\b/.test(line))) {
      if (!globs.includes("shared/**")) {
        problems.push(`${workflowName}: ${app}/go.mod replaces shared, but the workflow's paths: lacks 'shared/**' (a change in shared/ would not release ${app})`);
      }
    }

    for (const script of ["scripts/ci-test.sh", "scripts/release-stamp.sh"]) {
      if (!workflow.some(line => line.includes(script))) {
        problems.push(`${workflowName}: does not call ${script}`);
      }
    }

    const changelog = path.join(root, app, "CHANGELOG.md");
    if (!fs.existsSync(changelog)) {
      problems.push(`${app}/CHANGELOG.md: missing (release-${app}.yml stamps it)`);
    } else if (!readLines(changelog).some(line => /^## \[Unreleased\]/.test(line))) {
      problems.push(`${app}/CHANGELOG.md: has no "## [Unreleased]" heading, so the release stamp would fail`);
    }

    const dockerfile = path.join(root, app, "Dockerfile");
    if (fs.existsSync(dockerfile) && readLines(dockerfile).some(line => /^FROM\s+node[:@\s]/i.test(line)) && ciTest !== null) {
      const arm = ciTestArm(ciTest, app);
      if (arm === null) {
        problems.push(`scripts/ci-test.sh: no case arm for ${app}`);
      } else if (!arm.some(line => line.includes("shared/ui"))) {
        problems.push(`scripts/ci-test.sh: ${app}/Dockerfile has a Node (ui) stage but the ${app} case does not build shared/ui`);
      }
    }
  }
  return problems;
}

function main() {
  const root = path.resolve(process.argv[2] ?? path.join(path.dirname(fileURLToPath(import.meta.url)), ".."));
  const problems = checkCi(root);
  if (problems.length > 0) {
    console.error(`check-ci: ${problems.length} problem(s):`);
    for (const problem of problems) {
      console.error(`  - ${problem}`);
    }
    process.exit(1);
  }
  console.log("check-ci: ok");
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  main();
}
