// Package gamelift is the part of the game server that talks to Amazon GameLift Servers. If you are reading
// this sample to learn Amazon GameLift Servers, start here; the game itself (package game/...) is a small
// demo workload and you can skip it.
//
// Before anything runs, you deploy a fleet (where game servers run), a FlexMatch matchmaker (who plays
// whom), and a queue (which fleet location hosts a match); see lib/ in the CDK app. Then one match follows
// seven steps. The README, the code comments, and the numbered arrows in the architecture diagram use the
// same numbers:
//
//  1. A game server process starts and reports that it can host a match: InitSDK, then ProcessReady.
//     See RunProcess in process.go.
//  2. A player signs in to the Amazon Cognito user pool. See signIn in the web client (cmd/web/page/app.js).
//  3. The player asks your backend for a match, then checks the ticket until it is placed. See findMatch and
//     pollTicket in the web client.
//  4. The backend calls StartMatchmaking with the player ID from the verified token. See
//     lambda/matchmaking/index.mjs.
//  5. FlexMatch forms a match and the queue places it on a ready process. That creates a game session and
//     one player session per player; the backend returns the server address and the caller's own player
//     session ID. The rule set waits for two players; after WAIT_FOR_OPPONENT_SECONDS (15 by default) it accepts
//     one, so a lone player still
//     gets a match (see lib/matchmaking.ts).
//  6. Amazon GameLift Servers calls OnStartGameSession on that process. The process opens its port and calls
//     ActivateGameSession. The game session carries the matchmaker data, which lists the players FlexMatch
//     put in the match; if there is only one, the server plays the other seat itself. See StartSession in
//     lifecycle.go and humansIn in process.go.
//  7. The player connects straight to the game server and sends their player session ID; the server checks
//     it with AcceptPlayerSession before letting the player in, and then they play over the same connection.
//     See playerSessionValidator in lifecycle.go.
//
// Your own game plugs in through the Game interface in yourgame.go. Only yourgame.go knows the demo game;
// process.go, lifecycle.go, and sdk.go do not. To bring your own game, read the header of yourgame.go first.
//
// When the match ends, the server calls RemovePlayerSession for each player and ProcessEnding, then exits,
// and a fresh process starts for the next match (the service starts it on a managed fleet; scripts/play.sh
// does on the Anywhere fleet; see endMatch in lifecycle.go). Throughout, the service calls OnHealthCheck about
// once a minute (Healthy) and may call OnProcessTerminate when it needs the process to stop, for example
// during a scale-in (Terminate).
//
// Developer guide: https://docs.aws.amazon.com/gameliftservers/latest/developerguide/gamelift-sdk-interactions.html
package gamelift
