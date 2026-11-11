// Tests for the player API function (lambda/matchmaking/index.mjs). The Amazon GameLift Servers client is the real
// one with its `send` method replaced, so the commands the function builds are checked without calling the service.
// Run with: node --test test/*.test.mjs (npm test does).
import assert from 'node:assert/strict';
import { beforeEach, test } from 'node:test';
import { GameLiftClient } from '@aws-sdk/client-gamelift';

process.env.MATCHMAKING_CONFIGURATION_NAME = 'test-matchmaker';
const calls = [];
let reply = {};
// `reply` is an object for every call, or a function of the command name for different answers per call.
GameLiftClient.prototype.send = async function send(command) {
  calls.push({ name: command.constructor.name, input: command.input });
  const answer = typeof reply === 'function' ? reply(command.constructor.name, command.input) : reply;
  if (answer instanceof Error) throw answer; // the service answers an unknown ticket ID with an error
  return answer;
};
// What the real service does for a ticket ID it does not know.
const notFound = () => Object.assign(new Error('Matchmaking ticket not found: x'), { name: 'InvalidRequestException' });
const { handler } = await import('../lambda/matchmaking/index.mjs');

const ME = '11111111-1111-1111-1111-111111111111';
const OTHER = '22222222-2222-2222-2222-222222222222';
const event = (routeKey, { sub = ME, ticketId } = {}) => ({
  routeKey,
  pathParameters: ticketId ? { ticketId } : undefined,
  requestContext: { authorizer: { jwt: { claims: sub === null ? {} : { sub } } } },
});
const body = (response) => JSON.parse(response.body);

beforeEach(() => { calls.length = 0; reply = {}; });

test('a request without a verified token gets 401 and calls nothing', async () => {
  for (const sub of [null, 'not-a-uuid', `${ME}; DROP`]) {
    const response = await handler(event('POST /matchmaking', { sub }));
    assert.equal(response.statusCode, 401);
  }
  assert.equal(calls.length, 0);
});

test('POST queues the caller, using the ID from the token and a ticket that starts with it', async () => {
  reply = (name) => (name === 'StartMatchmakingCommand' ? { MatchmakingTicket: { Status: 'QUEUED' } } : notFound());
  const response = await handler(event('POST /matchmaking'));
  assert.equal(response.statusCode, 202);
  const { ticketId } = body(response);
  assert.ok(ticketId.startsWith(`${ME}-`));
  assert.match(ticketId, /^[0-9a-f-]{36}-\d{13}$/);
  assert.deepEqual(calls.map((c) => c.name), ['DescribeMatchmakingCommand', 'DescribeMatchmakingCommand', 'StartMatchmakingCommand']);
  assert.equal(calls[2].input.ConfigurationName, 'test-matchmaker');
  assert.deepEqual(calls[2].input.Players, [{ PlayerId: ME }]);
});

test("a player can't read or cancel another player's ticket", async () => {
  const theirs = `${OTHER}-1700000000000`;
  for (const routeKey of ['GET /matchmaking/{ticketId}', 'DELETE /matchmaking/{ticketId}']) {
    const response = await handler(event(routeKey, { ticketId: theirs }));
    assert.equal(response.statusCode, 404);
  }
  assert.equal((await handler(event('GET /matchmaking/{ticketId}', { ticketId: 'garbage' }))).statusCode, 404);
  assert.equal(calls.length, 0);
});

test("a completed ticket returns only the caller's player session", async () => {
  reply = {
    TicketList: [{
      Status: 'COMPLETED',
      GameSessionConnectionInfo: {
        IpAddress: '203.0.113.7', DnsName: 'example.invalid', Port: 33430,
        MatchedPlayerSessions: [
          { PlayerId: OTHER, PlayerSessionId: 'psess-other' },
          { PlayerId: ME, PlayerSessionId: 'psess-me' },
        ],
      },
    }],
  };
  const response = await handler(event('GET /matchmaking/{ticketId}', { ticketId: `${ME}-1700000000000` }));
  assert.equal(response.statusCode, 200);
  assert.deepEqual(body(response).connection, { ipAddress: '203.0.113.7', dnsName: 'example.invalid', port: 33430, playerSessionId: 'psess-me' });
  assert.ok(!response.body.includes('psess-other'));
});

test('a ticket that is still searching has no connection', async () => {
  reply = { TicketList: [{ Status: 'SEARCHING' }] };
  const response = await handler(event('GET /matchmaking/{ticketId}', { ticketId: `${ME}-1700000000000` }));
  assert.equal(body(response).status, 'SEARCHING');
  assert.equal(body(response).connection, undefined);
});

test('DELETE cancels the caller\'s own ticket', async () => {
  const ticketId = `${ME}-1700000000000`;
  const response = await handler(event('DELETE /matchmaking/{ticketId}', { ticketId }));
  assert.equal(response.statusCode, 200);
  assert.deepEqual(calls.map((c) => [c.name, c.input.TicketId]), [['StopMatchmakingCommand', ticketId]]);
});


// The function looks up this minute's ticket ID and last minute's; this tells them apart.
const lastMinute = (id) => Number(id.slice(-13)) < Math.floor(Date.now() / 60000) * 60000;

test('a player with a search already running cannot start another one', async () => {
  for (const status of ['QUEUED', 'SEARCHING', 'PLACING', 'REQUIRES_ACCEPTANCE']) {
    calls.length = 0;
    // The search started in the last minute is still running; nothing exists for this minute yet.
    reply = (name, input) => {
      if (name !== 'DescribeMatchmakingCommand') return {};
      const id = input.TicketIds[0];
      return lastMinute(id) ? { TicketList: [{ TicketId: id, Status: status }] } : notFound();
    };
    const response = await handler(event('POST /matchmaking'));
    assert.equal(response.statusCode, 429, status);
    assert.ok(!calls.some((c) => c.name === 'StartMatchmakingCommand'), `${status}: no ticket was started`);
  }
});

test('a finished or cancelled search from the last minute does not block a new one', async () => {
  for (const status of ['COMPLETED', 'CANCELLED', 'TIMED_OUT', 'FAILED']) {
    calls.length = 0;
    reply = (name, input) => {
      if (name === 'StartMatchmakingCommand') return { MatchmakingTicket: { Status: 'QUEUED' } };
      const id = input.TicketIds[0];
      return lastMinute(id) ? { TicketList: [{ TicketId: id, Status: status }] } : notFound();
    };
    assert.equal((await handler(event('POST /matchmaking'))).statusCode, 202, status);
  }
});

test("this minute's ticket ID can only be used once, even after the search was cancelled", async () => {
  reply = (name, input) => {
    if (name === 'StartMatchmakingCommand') return { MatchmakingTicket: { Status: 'QUEUED' } };
    return lastMinute(input.TicketIds[0]) ? notFound() : { TicketList: [{ TicketId: input.TicketIds[0], Status: 'CANCELLED' }] };
  };
  assert.equal((await handler(event('POST /matchmaking'))).statusCode, 429);
});

test('GET for a ticket the service does not know is a 404, not a server error', async () => {
  reply = () => notFound();
  const response = await handler(event('GET /matchmaking/{ticketId}', { ticketId: `${ME}-1700000000000` }));
  assert.equal(response.statusCode, 404);
});

test('a service error gives a generic message, not the details', async () => {
  const normalSend = GameLiftClient.prototype.send;
  GameLiftClient.prototype.send = async () => { throw Object.assign(new Error('secret detail'), { name: 'AccessDeniedException' }); };
  try {
    const response = await handler(event('POST /matchmaking'));
    assert.equal(response.statusCode, 502);
    assert.ok(!response.body.includes('secret detail'));
  } finally {
    GameLiftClient.prototype.send = normalSend;
  }
});

test('a throttled call to the service gives 429 so the page can try again', async () => {
  const normalSend = GameLiftClient.prototype.send;
  GameLiftClient.prototype.send = async () => { throw Object.assign(new Error('Rate exceeded'), { name: 'ThrottlingException' }); };
  try {
    const response = await handler(event('POST /matchmaking'));
    assert.equal(response.statusCode, 429);
  } finally {
    GameLiftClient.prototype.send = normalSend;
  }
});
