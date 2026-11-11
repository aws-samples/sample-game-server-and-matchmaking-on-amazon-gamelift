// Player API: steps 3 to 5 of the Amazon GameLift Servers flow (see server/gamelift/doc.go).
//
// Game clients never call Amazon GameLift Servers directly. They call this function through Amazon API Gateway,
// which has already checked the player's Amazon Cognito token. That way:
// - the player ID always comes from the verified token, never from the request, so nobody can queue as
//   someone else;
// - every ticket ID starts with that player ID, so a player can read or cancel only their own tickets;
// - a player gets back only their own player session ID, never the other player's.
//
// Routes:  POST /matchmaking               step 4, StartMatchmaking
//          GET /matchmaking/{ticketId}     step 5, DescribeMatchmaking (the client polls this)
//          DELETE /matchmaking/{ticketId}  StopMatchmaking, when the player cancels
import {
  GameLiftClient,
  StartMatchmakingCommand,
  DescribeMatchmakingCommand,
  StopMatchmakingCommand,
} from '@aws-sdk/client-gamelift';

const gamelift = new GameLiftClient({});
const CONFIGURATION_NAME = process.env.MATCHMAKING_CONFIGURATION_NAME;
const SUB_PATTERN = /^[0-9a-f-]{36}$/;
const TICKET_PATTERN = /^([0-9a-f-]{36})-\d{13}$/;
// One search per player at a time. The ticket ID is the player ID plus the start of the current minute, which is
// also how long a ticket lives (lib/matchmaking.ts), so a player's active ticket is always in this minute or the
// last one. Starting a new search is refused while either is still active, or when this minute's ID was already used.
const WINDOW_MS = 60_000;
const ACTIVE = new Set(['QUEUED', 'SEARCHING', 'REQUIRES_ACCEPTANCE', 'PLACING']);

const reply = (statusCode, body) => ({
  statusCode,
  headers: { 'content-type': 'application/json', 'cache-control': 'no-store' },
  body: JSON.stringify(body),
});

function callerId(event) {
  const sub = event.requestContext?.authorizer?.jwt?.claims?.sub;
  return typeof sub === 'string' && SUB_PATTERN.test(sub) ? sub : null;
}

// DescribeMatchmaking answers a ticket ID it does not know with an InvalidRequestException ("Matchmaking ticket not
// found") instead of an empty list, so look the tickets up one at a time and treat that error as "no such ticket".
async function findTicket(ticketId) {
  try {
    const out = await gamelift.send(new DescribeMatchmakingCommand({ TicketIds: [ticketId] }));
    return out.TicketList?.[0];
  } catch (err) {
    if (err.name === 'InvalidRequestException' && /not found/i.test(err.message)) return undefined;
    throw err;
  }
}

// Returns the ticket ID from the path only when it belongs to the caller.
function ownTicket(event, sub) {
  const ticketId = event.pathParameters?.ticketId ?? '';
  const match = TICKET_PATTERN.exec(ticketId);
  return match && match[1] === sub ? ticketId : null;
}

export async function handler(event) {
  const sub = callerId(event);
  if (!sub) return reply(401, { message: 'Sign in first.' });

  try {
    switch (event.routeKey) {
      case 'POST /matchmaking': {
        // Step 4. StartMatchmaking puts the player in the matchmaker's pool. The rule set (lib/matchmaking.ts)
        // decides who plays whom; here it is any two players, or one player alone after WAIT_FOR_OPPONENT_SECONDS.
        // You could also pass player attributes (skill) and latency to each fleet location for the rule set to use.
        // This pre-check is best effort: two requests in the same minute can both pass it, and then the second
        // StartMatchmaking fails because the ticket ID exists.
        const start = Math.floor(Date.now() / WINDOW_MS) * WINDOW_MS;
        const ticketId = `${sub}-${start}`;
        const [thisMinute, lastMinute] = await Promise.all([findTicket(ticketId), findTicket(`${sub}-${start - WINDOW_MS}`)]);
        if (thisMinute || (lastMinute && ACTIVE.has(lastMinute.Status))) {
          return reply(429, { message: 'You already have a match search. Cancel it, or try again in a minute.' });
        }
        const out = await gamelift.send(new StartMatchmakingCommand({
          ConfigurationName: CONFIGURATION_NAME,
          TicketId: ticketId,
          Players: [{ PlayerId: sub }],
        }));
        return reply(202, { ticketId, status: out.MatchmakingTicket?.Status });
      }
      case 'GET /matchmaking/{ticketId}': {
        // Step 5. The ticket moves through QUEUED, SEARCHING, and PLACING to COMPLETED once the queue has
        // started a game session on a ready server process. Then it carries the server address and one player
        // session per player.
        const ticketId = ownTicket(event, sub);
        if (!ticketId) return reply(404, { message: 'Ticket not found.' });
        const ticket = await findTicket(ticketId);
        if (!ticket) return reply(404, { message: 'Ticket not found.' });
        const body = { ticketId, status: ticket.Status };
        const info = ticket.GameSessionConnectionInfo;
        const mine = info?.MatchedPlayerSessions?.find((p) => p.PlayerId === sub);
        if (ticket.Status === 'COMPLETED' && info && mine) {
          // Return only this player's session, never the other player's.
          body.connection = { ipAddress: info.IpAddress, dnsName: info.DnsName, port: info.Port, playerSessionId: mine.PlayerSessionId };
        }
        return reply(200, body);
      }
      case 'DELETE /matchmaking/{ticketId}': {
        const ticketId = ownTicket(event, sub);
        if (!ticketId) return reply(404, { message: 'Ticket not found.' });
        await gamelift.send(new StopMatchmakingCommand({ TicketId: ticketId }));
        return reply(200, { ticketId, status: 'CANCELLED' });
      }
      default:
        return reply(404, { message: 'Not found.' });
    }
  } catch (err) {
    // Log the details for the operator; send the player a generic message.
    console.error(JSON.stringify({ route: event.routeKey, error: err.name, message: err.message }));
    // Too many calls to the service: tell the page to try again shortly.
    if (err.name === 'ThrottlingException') {
      return reply(429, { message: 'Too many requests. Try again in a moment.' });
    }
    return reply(502, { message: 'Matchmaking is not available right now. Try again.' });
  }
}
