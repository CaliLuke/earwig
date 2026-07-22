#!/usr/bin/env node
// Supported SDK-only reader. It deliberately emits raw payloads; Go owns normalization.
import path from 'node:path';

const sessionPageSize = 100;
const maxSessions = 50000;

export const sessionID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;

export function parseArgs(argv) {
  const [command, id] = argv;
  if (command === 'list' && argv.length === 1) {
    return {command};
  }
  const dirFlag = argv.indexOf('--dir');
  const dir = dirFlag >= 0 ? argv[dirFlag + 1] : null;
  if (command !== 'read' || argv.length !== 4 || dirFlag !== 2 || !path.isAbsolute(dir ?? '') || !sessionID.test(id ?? '')) {
    throw new Error('usage: claude-reader list | read SESSION_ID --dir ABSOLUTE_PATH');
  }
  return {command, id, dir: path.resolve(dir)};
}

export async function run(sdk, args) {
  if (args.command === 'list') {
    const sessions = [];
    for (let offset = 0; offset < maxSessions; offset += sessionPageSize) {
      const page = await sdk.listSessions({limit: sessionPageSize, offset});
      sessions.push(...page);
      if (page.length < sessionPageSize) {
        return sessions
          .sort((a, b) => (b.lastModified ?? 0) - (a.lastModified ?? 0))
          .map((session) => ({
            id: session.sessionId,
            summary: session.summary ?? null,
            git_branch: session.gitBranch ?? null,
            created_at: session.createdAt ?? null,
            last_modified: session.lastModified ?? null,
            cwd: session.cwd,
          }));
      }
    }
    throw new Error(`Claude session listing exceeds the ${maxSessions}-session safety limit`);
  }

  const info = await sdk.getSessionInfo(args.id, {dir: args.dir});
  if (!info || info.cwd !== args.dir) {
    throw new Error('Claude session was not found in this workspace');
  }
  const messages = await sdk.getSessionMessages(args.id, {dir: args.dir, includeSystemMessages: true});
  return {info, messages};
}

if (process.argv[1] === new URL(import.meta.url).pathname) {
  try {
    const args = parseArgs(process.argv.slice(2));
    const sdk = await import('@anthropic-ai/claude-agent-sdk');
    process.stdout.write(`${JSON.stringify(await run(sdk, args))}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 2;
  }
}
