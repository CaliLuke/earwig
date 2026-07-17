#!/usr/bin/env node
// Regenerates golden data from Auto-K's actual reference implementations.
//
// Usage:
//   AUTOK_SERVER_DIR=/path/to/autok-server node scripts/generate-reference-fixtures.mjs
//
// The checked-in outputs make `scripts/verify` hermetic. Regeneration requires
// only a local Auto-K checkout, Node, and Python; it never contacts a network.
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.dirname(scriptDir);
const candidates = [
  process.env.AUTOK_SERVER_DIR,
  path.resolve(repoRoot, '..', 'autok-deploy', 'autok-server'),
].filter(Boolean);
const autokRoot = candidates.find((candidate) => fs.existsSync(path.join(candidate, 'evals', 'providers', 'claude-transcript.mjs')));
if (!autokRoot) {
  throw new Error('Auto-K reference checkout not found; set AUTOK_SERVER_DIR');
}

const providersDir = path.join(autokRoot, 'evals', 'providers');
const { normalizeClaudeSession } = await import(pathToFileURL(path.join(providersDir, 'claude-transcript.mjs')));
const { normalizeCodexThread } = await import(pathToFileURL(path.join(providersDir, 'codex-thread.mjs')));
const readJSON = (relative) => JSON.parse(fs.readFileSync(path.join(repoRoot, relative), 'utf8'));
const claude = readJSON('testdata/fixtures/claude.json');
const codex = readJSON('testdata/fixtures/codex.json');
const normalized = {
  claude: normalizeClaudeSession({ info: claude.info, messages: claude.messages }),
  codex: normalizeCodexThread(codex.thread),
};

const python = spawnSync(
  process.env.PYTHON ?? 'python3',
  [
    path.join(scriptDir, 'reference_uuid_vectors.py'),
    path.join(autokRoot, 'evals', 'opik', 'src', 'autok_evals', 'cli.py'),
  ],
  { encoding: 'utf8' },
);
if (python.status !== 0) {
  throw new Error(`Auto-K UUID reference failed: ${python.stderr.trim()}`);
}
const vectors = JSON.parse(python.stdout);

const outputDir = path.join(repoRoot, 'testdata', 'generated');
fs.mkdirSync(outputDir, { recursive: true });
const writeJSON = (name, value) => {
  fs.writeFileSync(path.join(outputDir, name), `${JSON.stringify(value, null, 2)}\n`);
};
writeJSON('normalized.json', normalized);
writeJSON('trace-id-vectors.json', vectors);
process.stdout.write(`generated ${path.relative(repoRoot, outputDir)} from ${autokRoot}\n`);
