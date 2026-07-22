import test from 'node:test';
import assert from 'node:assert/strict';
import {parseArgs, run} from './index.mjs';

test('lists every project with pagination so workspace roots can be filtered by Go', async () => {
  const sessions = Array.from({length: 205}, (_, index) => ({
    sessionId: `session-${index}`,
    cwd: index % 2 === 0 ? `/workspace/project-${index}` : `/elsewhere/project-${index}`,
    lastModified: index,
  }));
  const calls = [];
  const sdk = {
    listSessions: async (options) => {
      calls.push(options);
      return sessions.slice(options.offset, options.offset + options.limit);
    },
  };

  const result = await run(sdk, parseArgs(['list']));

  assert.equal(result.length, sessions.length);
  assert.deepEqual(calls, [
    {limit: 100, offset: 0},
    {limit: 100, offset: 100},
    {limit: 100, offset: 200},
  ]);
  assert.equal(result[0].id, 'session-204');
  assert.equal(result.at(-1).id, 'session-0');
});

test('validates read arguments and includes system messages', async () => {
  assert.throws(() => parseArgs(['read', '../../bad', '--dir', '/tmp']), /usage/);
  let options;
  const sdk = {
    getSessionInfo: async () => ({cwd: '/tmp'}),
    getSessionMessages: async (_id, readOptions) => {
      options = readOptions;
      return [{type: 'system'}];
    },
  };
  const result = await run(sdk, parseArgs(['read', '0aaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee', '--dir', '/tmp']));
  assert.equal(result.messages[0].type, 'system');
  assert.equal(options.includeSystemMessages, true);
});
