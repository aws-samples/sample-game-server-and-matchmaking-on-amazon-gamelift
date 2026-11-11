'use strict';

// Browser client. The Amazon GameLift Servers flow comes first in this file:
//
//   Step 2  sign in to the Amazon Cognito user pool                  signIn
//   Step 3  ask the player API for a match, then check the ticket    findMatch, pollTicket
//   Step 7  connect to the game server with the player session ID    startGame
//
// The rest is the game: send "up", "down", or "stay", and draw the state the server sends. The server
// decides everything. Messages are described in game/match.go.
//
// Without a player API (local mode) the page skips sign-in and joins the local server directly.

const FIELD = { width: 800, height: 500, paddleHeight: 100, paddleWidth: 12, inset: 30, ballRadius: 8 };
const POLL_MS = 2000;

const $ = (id) => document.getElementById(id);
const canvas = $('table');
const ctx = canvas.getContext('2d');

let config = null;
let session = null; // { username, accessToken, expiresAt }, kept in memory only

function setStatus(text) {
  if ($('status').textContent !== text) $('status').textContent = text;
}

function show(view) {
  for (const id of ['signin', 'lobby', 'game']) $(id).hidden = id !== view;
}

// ---------- Step 2: sign in (Amazon Cognito user pool) ----------
//
// The player API accepts only a token from this user pool and takes the player ID from it. A production title signs
// players in with its platform identity instead; the matchmaking part stays the same.

async function signIn(username, password) {
  const res = await fetch(`https://cognito-idp.${config.region}.amazonaws.com/`, {
    method: 'POST',
    headers: {
      'content-type': 'application/x-amz-json-1.1',
      'x-amz-target': 'AWSCognitoIdentityProviderService.InitiateAuth',
    },
    body: JSON.stringify({
      AuthFlow: 'USER_PASSWORD_AUTH',
      ClientId: config.clientId,
      AuthParameters: { USERNAME: username, PASSWORD: password },
    }),
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.message || 'Sign-in failed.');
  if (body.ChallengeName) throw new Error(`This account needs the ${body.ChallengeName} step, which this page doesn't support.`);
  const result = body.AuthenticationResult;
  if (!result || !result.AccessToken) throw new Error('Sign-in failed.');
  return { username, accessToken: result.AccessToken, expiresAt: Date.now() + result.ExpiresIn * 1000 };
}

function signedIn() {
  return session && Date.now() < session.expiresAt - 30000;
}

// ---------- Step 3: find a match through the player API ----------
//
// The page never calls Amazon GameLift Servers itself. It calls the player API (lambda/matchmaking), which
// calls StartMatchmaking with the signed-in player's ID, so nobody can start matchmaking as someone else.

// s is the sign-in the call belongs to. A call that outlives a sign-out still uses the token it started with, and
// a 401 for an old sign-in does not sign out a newer one.
async function api(method, path, s = session) {
  const res = await fetch(`${config.apiUrl}${path}`, {
    method,
    headers: { authorization: `Bearer ${s.accessToken}` },
  });
  const body = await res.json().catch(() => ({}));
  if (res.status === 401) {
    if (session === s) session = null;
    throw new Error('Your sign-in expired. Sign in again.');
  }
  if (!res.ok) throw new Error(body.message || `Request failed (${res.status}).`);
  return body;
}

const STATUS_TEXT = {
  QUEUED: 'Waiting in the queue...',
  SEARCHING: 'Looking for another player...',
  PLACING: 'Match found. Starting the game server...',
  REQUIRES_ACCEPTANCE: 'Match found. Waiting for acceptance...',
};

let ticket = null; // { id, timer, failures, session }; id is null until the start request returns
let epoch = 0; // counts sign-outs, so a Cancel that finishes after a sign-out leaves the screen alone

// Step 3: ask for a match. The API starts matchmaking (step 4) and returns a ticket ID to check on.
async function findMatch() {
  $('find').disabled = true;
  $('cancel').hidden = false;
  $('cancel').disabled = false;
  setStatus('Requesting a match...');
  const t = { id: null, timer: null, session }; // set before the request, so Cancel and Sign out work while it runs
  ticket = t;
  try {
    const out = await api('POST', '/matchmaking', t.session);
    if (ticket !== t) {
      // Cancelled or signed out while the request was in flight: stop the search that just started.
      api('DELETE', `/matchmaking/${encodeURIComponent(out.ticketId)}`, t.session).catch(() => {});
      return;
    }
    t.id = out.ticketId;
    pollTicket();
  } catch (err) {
    if (ticket === t) endSearch(err.message);
  }
}

// Step 3, continued: check the ticket every two seconds. When FlexMatch has formed the match and the queue has placed it
// on a game server, the status is COMPLETED and the reply has the server address and this player's own
// player session ID. (For many players, have the backend store the FlexMatch events instead of polling.)
async function pollTicket() {
  const t = ticket; // the search this poll belongs to; a later search must not inherit its reply
  if (!t) return;
  try {
    const out = await api('GET', `/matchmaking/${encodeURIComponent(t.id)}`, t.session);
    if (ticket !== t) return; // cancelled (or replaced by a new search) while waiting for the reply
    t.failures = 0;
    if (out.status === 'COMPLETED' && !out.connection) {
      endSearch('The match is ready, but the reply has no server address. Try again.');
      return;
    }
    if (out.status === 'COMPLETED' && out.connection) {
      const c = out.connection;
      // A fleet with TLS certificates gives a DNS name that matches its certificate; connect to it with wss://.
      // Without one, the page connects unencrypted only to this machine (this sample's Anywhere fleet), never
      // to another address.
      if (!c.dnsName && c.ipAddress !== '127.0.0.1') {
        endSearch('The game server has no TLS name, so this page will not connect to it without encryption.');
        return;
      }
      ticket = null;
      $('find').disabled = false;
      $('cancel').hidden = true;
      // Encrypted whenever the game session has a TLS name. Without one, the guard above has already limited
      // this to 127.0.0.1, so no traffic leaves this machine.
      const scheme = c.dnsName ? 'wss' : 'ws';
      const url = `${scheme}://${c.dnsName || c.ipAddress}:${c.port}`;
      startGame(url, c.playerSessionId);
      return;
    }
    if (['FAILED', 'TIMED_OUT', 'CANCELLED'].includes(out.status)) {
      endSearch(out.status === 'TIMED_OUT' ? 'No game server was ready in time. Check that the game server is running, then try again.' : `Matchmaking ended: ${out.status}.`);
      return;
    }
    setStatus(STATUS_TEXT[out.status] || `Matchmaking: ${out.status}`);
  } catch (err) {
    // One failed check (a throttled call, a brief network error) should not abandon a search that is still
    // running on the service. Give up after five failures in a row and cancel the ticket.
    if (ticket !== t) return;
    if (!session) { // the sign-in expired (api() cleared it), so retrying cannot help
      endSearch(err.message);
      return;
    }
    t.failures = (t.failures || 0) + 1;
    if (t.failures >= 5) {
      api('DELETE', `/matchmaking/${encodeURIComponent(t.id)}`, t.session).catch(() => {}); // best effort
      endSearch(err.message);
      return;
    }
  }
  t.timer = setTimeout(pollTicket, POLL_MS);
}

// quiet is for Sign out, which shows its own message and view.
async function cancelSearch(quiet = false) {
  if (!ticket) return;
  const { id, timer, session: s } = ticket;
  const startedAt = epoch;
  clearTimeout(timer);
  ticket = null;
  const finish = (message) => {
    if (quiet || epoch !== startedAt) {
      // Sign out showed its own view. Touch the buttons only if no newer search has started since.
      if (!ticket) {
        $('find').disabled = false;
        $('cancel').hidden = true;
      }
    } else {
      endSearch(message);
    }
  };
  if (!id) return finish('Search cancelled.'); // the start request is still running; findMatch cancels it when it returns
  try {
    setStatus('Cancelling...');
    $('cancel').disabled = true;
    await api('DELETE', `/matchmaking/${encodeURIComponent(id)}`, s);
    finish('Search cancelled.');
  } catch (err) {
    finish(err.message);
  }
}

function endSearch(message) {
  if (ticket) clearTimeout(ticket.timer);
  ticket = null;
  $('find').disabled = false;
  $('cancel').hidden = true;
  setStatus(message);
  if (!session) enterSignIn();
}

// ---------- Step 7: join the game server, then play ----------

let game = null; // { socket, seat, state, over }

// Step 7: connect straight to the game server and join with the player session ID from matchmaking. The
// server checks it with AcceptPlayerSession and refuses an unknown or already used ID.
function startGame(url, playerSessionId) {
  stopGame();
  const g = { socket: new WebSocket(url), seat: -1, state: null, over: false };
  game = g;
  show('game');
  $('again').hidden = true;
  setStatus('Joining the game server...');
  g.socket.onopen = () => g.socket.send(JSON.stringify({ type: 'join', playerSessionId }));
  g.socket.onmessage = (event) => {
    const msg = JSON.parse(event.data);
    if (msg.type === 'joined') g.seat = msg.seat;
    if (msg.type === 'rejected') endGame(g, `The game server refused this player: ${msg.reason}.`);
    if (msg.type === 'state') {
      g.state = msg;
      if (msg.phase === 'finished') endGame(g, '');
    }
  };
  g.socket.onclose = () => endGame(g, 'Disconnected from the game server.');
}

function endGame(g, message) {
  if (g !== game || g.over) return;
  g.over = true;
  if (message) setStatus(message);
  $('again').hidden = false;
  $('again').focus();
}

function stopGame() {
  if (!game) return;
  game.over = true;
  game.socket.close();
  game = null;
}

// Input: the arrow keys or W and S. Send the direction whenever it changes, and a few times a second anyway
// so the server sees the player is still there.
const keys = new Set();
addEventListener('keydown', (e) => {
  if (!$('game').hidden && e.code.startsWith('Arrow')) e.preventDefault();
  keys.add(e.code);
});
addEventListener('keyup', (e) => keys.delete(e.code));
addEventListener('blur', () => keys.clear());

let lastDir = null;
let lastSent = 0;
function sendMove(now) {
  const g = game;
  if (!g || g.over || g.seat < 0 || g.socket.readyState !== WebSocket.OPEN) return;
  const dir = (keys.has('ArrowDown') || keys.has('KeyS') ? 1 : 0) - (keys.has('ArrowUp') || keys.has('KeyW') ? 1 : 0);
  if (dir !== lastDir || now - lastSent > 250) {
    g.socket.send(JSON.stringify({ type: 'move', dir }));
    lastDir = dir;
    lastSent = now;
  }
}

// ---------- Drawing ----------

function draw() {
  const s = game && game.state;
  ctx.clearRect(0, 0, FIELD.width, FIELD.height);
  ctx.fillStyle = 'rgba(255,255,255,0.3)';
  for (let y = 0; y < FIELD.height; y += 30) ctx.fillRect(FIELD.width / 2 - 2, y, 4, 18);
  if (!s || game.seat < 0) return;

  const colors = ['#4aa3ff', '#ff6b6b'];
  const x = [FIELD.inset - FIELD.paddleWidth, FIELD.width - FIELD.inset];
  s.paddles.forEach((y, k) => {
    ctx.fillStyle = colors[k];
    ctx.fillRect(x[k], y - FIELD.paddleHeight / 2, FIELD.paddleWidth, FIELD.paddleHeight);
  });
  ctx.fillStyle = '#fff';
  ctx.beginPath();
  ctx.arc(s.ballX, s.ballY, FIELD.ballRadius, 0, Math.PI * 2);
  ctx.fill();
  ctx.textAlign = 'center';
  ctx.font = 'bold 40px system-ui, sans-serif';
  ctx.fillText(`${s.score[0]}   ${s.score[1]}`, FIELD.width / 2, 50);

  if (game.over && s.phase !== 'finished') return; // the connection ended; keep the message endGame set
  const side = game.seat === 0 ? 'left, blue' : 'right, red';
  if (s.phase === 'waiting') setStatus(`You are on the ${side}. Waiting for the other player...`);
  else if (s.phase === 'serving') setStatus(`You are on the ${side}. Get ready...`);
  else if (s.phase === 'playing') setStatus(`You are on the ${side}.`);
  else {
    const text = s.winner === game.seat ? 'You win!' : 'You lose.';
    setStatus(text);
    ctx.font = 'bold 48px system-ui, sans-serif';
    ctx.fillText(text, FIELD.width / 2, FIELD.height / 2);
  }
}

function frame(now) {
  requestAnimationFrame(frame);
  if ($('game').hidden) return;
  sendMove(now);
  draw();
}

// ---------- Views ----------

function enterSignIn() {
  stopGame();
  show('signin');
  $('who').textContent = '';
  $('password').value = '';
  $('username').focus();
}

function enterLobby() {
  stopGame();
  show('lobby');
  $('find').disabled = false;
  $('cancel').hidden = true;
  $('cancel').disabled = false;
  $('find').focus();
  if (config.mode === 'online') {
    $('who').textContent = `Signed in as ${session.username}`;
    $('find').textContent = 'Find match';
    $('lobby-text').textContent = 'Press Find match to play another player.';
    $('signout').hidden = false;
  } else {
    $('who').textContent = 'Local mode';
    $('find').textContent = 'Join match';
    $('lobby-text').textContent = 'This page joins the local game server directly. Open this page in a second browser window side by side, or start a bot, to get an opponent.';
    $('signout').hidden = true;
  }
}

$('signin-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const button = e.submitter;
  button.disabled = true;
  setStatus('Signing in...');
  try {
    session = await signIn($('username').value.trim(), $('password').value);
    setStatus('');
    enterLobby();
  } catch (err) {
    setStatus(err.message);
  } finally {
    button.disabled = false;
    $('password').value = '';
  }
});

$('find').addEventListener('click', () => {
  if (config.mode !== 'online') {
    startGame(config.localServer, 'local-' + crypto.randomUUID());
    return;
  }
  if (!signedIn()) {
    session = null;
    setStatus('Your sign-in expired. Sign in again.');
    enterSignIn();
    return;
  }
  findMatch();
});
$('cancel').addEventListener('click', () => cancelSearch());
$('signout').addEventListener('click', () => {
  epoch++;
  cancelSearch(true);
  session = null;
  setStatus('Signed out.');
  enterSignIn();
});
$('again').addEventListener('click', () => {
  setStatus('');
  if (config.mode === 'online' && !signedIn()) enterSignIn();
  else enterLobby();
});

(async () => {
  try {
    config = await (await fetch('config.json', { cache: 'no-store' })).json();
  } catch {
    setStatus('Could not load the page settings.');
    return;
  }
  if (config.mode === 'online') enterSignIn();
  else enterLobby();
  requestAnimationFrame(frame);
})();
