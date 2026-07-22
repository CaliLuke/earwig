#!/usr/bin/env node
import fs from 'node:fs';

const packageJSON = JSON.parse(fs.readFileSync('helpers/claude-reader/package.json', 'utf8'));
const packageLock = JSON.parse(fs.readFileSync('helpers/claude-reader/package-lock.json', 'utf8'));
const npmConfig = fs.readFileSync('helpers/claude-reader/.npmrc', 'utf8');
const dependency = '@anthropic-ai/claude-agent-sdk';
const declared = packageJSON.dependencies?.[dependency];
if (typeof declared !== 'string' || /^[~^]/.test(declared)) {
  throw new Error(`${dependency} must be pinned exactly; got ${JSON.stringify(declared)}`);
}
const lockDeclared = packageLock.packages?.['']?.dependencies?.[dependency];
const lockInstalled = packageLock.packages?.[`node_modules/${dependency}`]?.version;
if (lockDeclared !== declared || lockInstalled !== declared) {
  throw new Error(`package-lock mismatch: package=${declared} lock=${lockDeclared} installed=${lockInstalled}`);
}
if (!/^legacy-peer-deps=true$/m.test(npmConfig)) {
  throw new Error('helper npm installs must omit unused SDK peer dependencies');
}
process.stdout.write(`PASS helper SDK pin ${declared}\n`);
