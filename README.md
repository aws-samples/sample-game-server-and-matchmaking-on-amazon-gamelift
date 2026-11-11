# Game Server and Matchmaking on Amazon GameLift Servers

This sample shows, in a small amount of commented code, how a multiplayer game uses Amazon GameLift Servers: how a game server process reports to the service, how players are matched with Amazon GameLift Servers FlexMatch, how a match is placed on a server, and how the server checks the player session ID that each player brings. Two-player Pong is the demo game, and you can play it in your browser.

> **Disclaimer:** This is sample code, for non-production usage. You should work with your security and legal teams to meet your organizational security, regulatory, and compliance requirements before deployment.

## What you learn

- The game server lifecycle with the Go server software development kit (SDK): `InitSDK`, `ProcessReady`, `OnStartGameSession`, `ActivateGameSession`, `AcceptPlayerSession`, `RemovePlayerSession`, `ProcessEnding`, and the health check and terminate callbacks. The sample uses the Go server SDK to keep the server short. Amazon GameLift Servers also provides server SDKs for C++ and C#, and plugins for Unreal Engine and Unity, with the same lifecycle calls, so the steps here map onto a server written in those.
- The resources a match needs: a fleet, a FlexMatch rule set and matchmaking configuration, and a game session queue, deployed together with the AWS Cloud Development Kit (AWS CDK).
- Why game clients call your own backend instead of Amazon GameLift Servers, and how that backend starts and checks matchmaking for a signed-in player.
- How FlexMatch waits for a real opponent and then relaxes its rule (an expansion), and how the game server plays the empty seat when none arrives.
- How to run the whole flow against the real service from your own machine with an Anywhere fleet, before you upload a build to a managed fleet.

## Amazon GameLift Servers in six terms

| Term | What it is |
|---|---|
| Fleet | A set of machines (computes) that run your game server, in one or more locations |
| Game server process | One running copy of your server. A compute can run several, each on its own port |
| Game session | One match. A process hosts one game session at a time |
| Player session | One player's seat in a game session. Players send the player session ID when they connect |
| Queue and matchmaker | FlexMatch groups players into a match; the queue picks the fleet location that hosts it |
| Matchmaking ticket | One player's match request. `StartMatchmaking` creates it, and your backend checks its status until it holds the game session's address |

## Which fleet type to use

| | Managed Amazon Elastic Compute Cloud (Amazon EC2) fleet | Managed container fleet | Anywhere fleet |
|---|---|---|---|
| Who runs the machines | Amazon GameLift Servers | Amazon GameLift Servers | You (your laptop, data center, or other hosting) |
| What you provide | A server build | A container image | The machines, and you start the processes (or run the Amazon GameLift Servers Agent, a program that starts and restarts your processes on Anywhere computes) |
| Scaling | Built in | Built in | Yours |
| How players reach the server | The instance's public address, limited by the fleet's inbound permissions | Same as EC2 | Wherever your machine is |
| Good for | Production, when you ship a server build | Production, when you ship container images | Development and testing, and production on hardware you run (on-premises or other hosting) |

The game server code is the same for each fleet type. Only who runs the machines and who starts the processes changes.

**This sample uses an Anywhere fleet by default.** You register your own machine with the fleet and run the game server on it, so the whole flow runs against the real service without uploading a build or opening a port to the network. To run the same server on a managed Amazon EC2 fleet instead, build it and deploy with `-c fleetType=ec2 -c allowedCidr=<your IP>/32`, where `<your IP>` is your public Internet Protocol (IP) address (see [From Anywhere to a managed fleet](#from-anywhere-to-a-managed-fleet)).

## Where each part runs

In this sample the game server runs **on your own machine**, and the matchmaking and sign-in run **in your AWS account**.

| Part | Where it runs | Address |
|---|---|---|
| Game server process (`server/cmd/server`) | Your machine, registered as a compute of the Anywhere fleet | `ws://127.0.0.1:33430` |
| Web page (`server/cmd/web`) | Your machine | `http://127.0.0.1:8080` |
| Amazon Cognito user pool (sign-in) | Your AWS account | regional service endpoint |
| Amazon API Gateway and AWS Lambda (player API) | Your AWS account | `ApiUrl` stack output |
| FlexMatch matchmaker, game session queue, Anywhere fleet | Your AWS account (Amazon GameLift Servers) | service API |

You register your machine with the address `127.0.0.1`, so the service tells players to connect to `127.0.0.1:33430`. That works because the players' browser runs on the same machine. The game server opens its own connection out to Amazon GameLift Servers (to report that it is ready, and to receive game sessions), so nothing on your machine accepts connections from the network. With `fleetType=ec2`, the game server runs on an Amazon EC2 instance that Amazon GameLift Servers manages, and players connect to the game session's Domain Name System (DNS) name over `wss://` (see [From Anywhere to a managed fleet](#from-anywhere-to-a-managed-fleet)).

## What each part is responsible for

| Part | Responsible for | Not responsible for |
|---|---|---|
| **Game client** (the web page) | Signing the player in, asking your backend for a match, connecting to the game server it is given, sending the player's moves, and drawing the state the server sends | Calling Amazon GameLift Servers, or deciding anything about the game |
| **Your backend** (Amazon Cognito, Amazon API Gateway, AWS Lambda) | Proving who the player is, then calling Amazon GameLift Servers on the player's behalf: start matchmaking with the player ID from the verified token, check the ticket, and return only that player's own connection details | Game logic, or carrying game traffic |
| **Amazon GameLift Servers** (FlexMatch, queue, fleet) | Knowing the address of each compute and the port and state of each server process, forming matches, picking a ready process, creating the game session and one player session per player, checking player session IDs when the server asks, health checks, and (on managed fleets) starting processes and scaling machines | Carrying game traffic: in this sample players connect straight to the game server, and gameplay doesn't pass through the service |
| **Game server process** | Reporting ready, hosting the one match it is given, letting in only players whose player session ID the service accepts, running the game rules, sending the state to the players, and reporting the end of the match | Matchmaking, or knowing who the players are beyond their player session IDs |

A useful way to think about it: in this sample Amazon GameLift Servers is the dispatcher, not a relay. It keeps track of where servers are and which seats belong to which match, and it tells your backend where to send each player. The players then talk to the game server directly. (Managed fleets can also turn on player gateway, an optional relay network for User Datagram Protocol (UDP) traffic that helps protect game servers from distributed denial of service (DDoS) traffic; this sample doesn't use it.)

How the service knows a server's address depends on the fleet type. On an Anywhere fleet you give it when you register the compute with `RegisterCompute` (this sample uses `127.0.0.1`, as above). On a managed fleet each instance gets a public address from AWS, and the service uses that. Each process adds its own port when it calls `ProcessReady`.

The game server and the service talk over one connection that the game server opens when it calls `InitSDK`. Everything the service asks of the process (start a game session, check a player session, report health) travels back over that connection, so the service doesn't need to reach your machine; your machine only needs to reach the service. That is why health checks work on `127.0.0.1`: about once a minute the SDK calls `OnHealthCheck` and sends the answer over that connection. A process that doesn't answer within 60 seconds counts as unhealthy. If a process stays unhealthy for three health checks in a row, the service might shut it down and start a new one. On a managed fleet the service starts that new process; on an Anywhere fleet starting processes is up to you (or the Amazon GameLift Servers Agent), and `scripts/play.sh` starts a new process only when the old one exits.

## How a match flows

Before a match, you deploy a fleet, a FlexMatch matchmaker, and a game session queue, in [lib/](lib/). Then each match follows the seven steps below. The step numbers are the numbered arrows in the [architecture diagram](#architecture), and the code comments and the game server's log lines use the same numbers.

| Step | What happens | Where to read it |
|---|---|---|
| 1 | A game server process starts, connects with `InitSDK` (the code calls `InitSDKFromEnvironment`, the form that reads the connection settings from environment variables), and reports that it can host a match with `ProcessReady` | [server/gamelift/process.go](server/gamelift/process.go) `RunProcess` |
| 2 | A player signs in to the Amazon Cognito user pool | [web client](server/cmd/web/page/app.js) `signIn` |
| 3 | The player asks your backend for a match, then checks the ticket until the match is placed | [web client](server/cmd/web/page/app.js) `findMatch`, `pollTicket`, [lib/player-api.ts](lib/player-api.ts) |
| 4 | The backend calls `StartMatchmaking` with the player ID from the verified token, not one from the request | [lambda/matchmaking/index.mjs](lambda/matchmaking/index.mjs) `POST` route |
| 5 | FlexMatch forms a match, and the queue places it on a ready process. That creates a game session and one player session per player. The rule set waits up to 15 seconds for a second player and then accepts a match of one ([Playing alone](#playing-alone)). The backend returns the server address and the caller's own player session ID | [lib/matchmaking.ts](lib/matchmaking.ts), [lambda/matchmaking/index.mjs](lambda/matchmaking/index.mjs) `GET` route |
| 6 | The service calls `OnStartGameSession` on that process. The process counts the match's players in the game session's matchmaker data (a JavaScript Object Notation (JSON) list of the players FlexMatch put in the match), opens its port, and calls `ActivateGameSession` | [server/gamelift/lifecycle.go](server/gamelift/lifecycle.go) `StartSession` |
| 7 | The player connects straight to the game server with their player session ID, and the server checks it with `AcceptPlayerSession`. Then they play over the same connection: the page sends moves, and the server sends the game state 60 times a second | [web client](server/cmd/web/page/app.js) `startGame`, [server/gamelift/lifecycle.go](server/gamelift/lifecycle.go) `playerSessionValidator`, [server/game/match.go](server/game/match.go) |

The flowchart below follows one game server process from start to exit. The shaded step is the one that depends on the match's player count.

![Flowchart: a game server process starts, calls InitSDK and ProcessReady (step 1), and receives OnStartGameSession (step 6). It opens its port and calls ActivateGameSession. If the match has one player, the server checks that player with AcceptPlayerSession (step 7) and seats a computer player when that player joins; otherwise it checks each player with AcceptPlayerSession (step 7) and waits for the second player. After the match, it calls RemovePlayerSession for each player and ProcessEnding, then exits, and a new process starts](server-process-flow.png)


When the match ends, the server calls `RemovePlayerSession` for each player and `ProcessEnding`, then exits, and a fresh process starts for the next match: `scripts/play.sh` starts it on the Anywhere fleet, and the service starts it on a managed fleet ([lifecycle.go](server/gamelift/lifecycle.go) `endMatch`). While a process runs, the service also calls `OnHealthCheck` about once a minute and can call `OnProcessTerminate` when it needs the process to stop (`Host.Healthy` and `Host.Terminate` in `lifecycle.go`).

## Architecture

![Architecture diagram: on your machine, a web browser signs in to Amazon Cognito (2) and asks Amazon API Gateway for a match (3; the server address comes back on the same ticket check, step 5); an AWS Lambda function calls StartMatchmaking on the FlexMatch matchmaker (4), which waits up to 15 seconds for a second player and then accepts a match of one, and hands the match to the game session queue (5); the queue starts a game session on the game server process over the connection the process opened in step 1 (6), which earlier reported ready to the Anywhere fleet (1); the game server counts the match's players and plays the empty seat itself if there is only one; the browser then connects to the game server, joins with its player session ID, and plays (7). As an alternative, with fleetType=ec2 the queue places matches on a managed EC2 fleet, where the game server runs on an EC2 instance the service manages and players connect with wss:// only from allowedCidr](architecture-diagram.png)

The numbers are the steps in [How a match flows](#how-a-match-flows). Orange arrows are the game server's lifecycle; dark arrows are the player's path. The dashed arrow is the alternative placement on a managed EC2 fleet. Arrow 6 travels over the connection the game server opened in step 1; the service doesn't open a connection to your machine.

## Read the code in this order

1. [server/gamelift/doc.go](server/gamelift/doc.go): the seven steps on one page, with pointers into the code.
2. [server/gamelift/process.go](server/gamelift/process.go): each server SDK call in the order the service expects them.
3. [server/gamelift/lifecycle.go](server/gamelift/lifecycle.go): what the server does in each callback.
4. [server/gamelift/yourgame.go](server/gamelift/yourgame.go): the four methods a game provides, and where the demo game plugs in. Read this one to bring your own game ([Use your own game](#use-your-own-game)).
5. [lambda/matchmaking/index.mjs](lambda/matchmaking/index.mjs): the backend's matchmaking calls.
6. [lib/](lib/): the AWS resources, one file per part.
7. [server/cmd/web/page/app.js](server/cmd/web/page/app.js): the client side of steps 2, 3, and 7.

You can skip [server/game/](server/game/): it is the demo game (Pong rules in `pong.go`, the WebSocket match loop in `match.go`, the computer player in `ai.go`) and contains no Amazon GameLift Servers code. Unit tests for the lifecycle use a fake service ([lifecycle_test.go](server/gamelift/lifecycle_test.go)), which is a quick way to see the expected order of calls.

## Use your own game

Pong is a placeholder. The Amazon GameLift Servers part of the server is generic, and it reaches your game through one small interface, so you can keep it and replace the game.

![Diagram of the three parts of the game server: the Amazon GameLift Servers part in server/gamelift (process.go, lifecycle.go, sdk.go) that you keep; yourgame.go with NewGame and the Game interface (Listen, Run, LastTick, Close) that you change; and your game in server/game with its network protocol, rules, and seats that you replace. The lifecycle code gives NewGame the setup (A), calls Listen and Run on the Game (B), and your game implements the interface (C)](game-boundary.png)

| Part | What you do |
|---|---|
| [server/gamelift/](server/gamelift/) (`process.go`, `lifecycle.go`, `sdk.go`) | Keep. It reports the process to the service, hosts one game session, checks each player, removes the player sessions, and ends the process. Its code doesn't depend on Pong |
| [server/gamelift/yourgame.go](server/gamelift/yourgame.go) | Change. It defines `Game`, `Setup`, and `NewGame`, and `PongGame`, the demo's `NewGame`. Write your own `NewGame` next to it |
| [server/game/](server/game/) | Replace with your game: the network protocol, the rules, the game loop, and the seats. It needs no Amazon GameLift Servers code |
| [server/cmd/server/main.go](server/cmd/server/main.go) | Change the `gamelift.New` call to pass your `NewGame` instead of `PongGame`, and drop the Pong-only `-score` flag and the local mode |
| [lib/](lib/) and [lambda/](lambda/) | Keep, and adjust the rule set and the game properties in `lib/matchmaking.ts` for your match. Skill and latency rules also need the backend to send each player's attributes and latency in `StartMatchmaking` ([lambda/matchmaking/index.mjs](lambda/matchmaking/index.mjs)), and a different team size needs your game to handle that many seats |
| [server/cmd/web/](server/cmd/web/) | Replace with your client. Keep the sign-in and matchmaking calls (steps 2 to 4) and the join with the player session ID (step 7) |

Your game provides a factory (`NewGame`) and the four methods of the `Game` it makes: `Listen`, `Run`, `LastTick`, and `Close`. The header of [yourgame.go](server/gamelift/yourgame.go) explains each call, the order, and what your game has to do in `Run` (check each player, stop when the context is canceled, and report the accepted player sessions). That header is where the order of the `Game` calls is written down, so read it before you write your game.

To see the smallest possible game, read `toyGame` in [lifecycle_test.go](server/gamelift/lifecycle_test.go). It has about 35 lines, no network protocol, and it passes through the same lifecycle as Pong.

Your game keeps these responsibilities, because the sample can't decide them for you:

- **The network protocol.** This sample uses WebSocket and JSON so a browser can connect. A console title usually sends UDP packets, or uses the platform's own networking, and then the port you open in `Listen` and the player check in `Run` change, but the steps do not.
- **Seats and leaving.** Your game decides which connected player gets which seat, and what happens when a player disconnects in the middle of a match. Pong gives the first free seat. A player who leaves before the match starts frees the seat, and a player who leaves during the match forfeits.
- **Who is allowed in.** Call `Setup.Validator.Validate` before you give a player anything. That check is what keeps someone who knows your server's address, but was not matched, out of the match.
- **Match size.** `Setup.Humans` tells you how many players FlexMatch put in the match. If your rule set can start a match with fewer players than the game needs, decide what your game does with the empty seats (Pong uses a computer player).

### If your game uses UDP

Console games usually send gameplay over UDP with a native client, not WebSocket in a browser. The Amazon GameLift Servers part is the same, because in this sample the service doesn't carry your game's packets. Only these parts change:

| Part | What changes |
|---|---|
| Fleet inbound permissions | Open the game port for `UDP` instead of `TCP` (Transmission Control Protocol). On the managed EC2 fleet this is the `protocol` in [lib/ec2-fleet.ts](lib/ec2-fleet.ts). Keep the source range narrow while you test |
| `Listen` in your `Game` | Open a UDP socket (`net.ListenPacket` in Go) on `Setup.Port` instead of an HTTP listener |
| Joining | There is no connection, so the client sends the player session ID in its first packet. Your game calls `Setup.Validator.Validate` for it, remembers which client address got which seat, and ignores packets from addresses it has not accepted. `Validate` calls the service, so run it off your game loop |
| Leaving | A closed connection no longer tells you a player left. Treat a long silence from an address as a disconnect |
| Address the client uses | The player API already returns both `ipAddress` and `dnsName`. A native client connects to the IP address and the port; only the browser needs the DNS name, for `wss://` |
| Encryption | The fleet's generated certificate lets clients verify the game server and encrypt traffic with Transport Layer Security (TLS); with `wss://`, the WebSocket library does the encryption. For UDP, your game sets up the encryption itself, for example with Datagram Transport Layer Security (DTLS) or the encryption in your networking library or platform |

Steps 1 to 6 (reporting ready, matchmaking, placement, `OnStartGameSession`, `ActivateGameSession`) and the cleanup after the match do not change. Player gateway, an optional relay network for UDP traffic ([What each part is responsible for](#what-each-part-is-responsible-for)), changes this: you turn it on when you create a managed fleet, and your backend and client then handle its connection details and tokens. See the Amazon GameLift Servers documentation on player gateway before you choose it.

Before you take a title to production, plan for the parts this sample leaves out: autoscaling and more than one location, latency and skill rules in the rule set, FlexMatch backfill so waiting players join running matches, real player sign-in with your platform's identity, and a managed container fleet if you ship your server as a container image. [Limitations](#limitations) covers several of them.

## Prerequisites

- An AWS account with permission to create the stack's resources, and the AWS Command Line Interface (AWS CLI) v2 configured for the AWS Region you deploy to. Use a Region where Amazon GameLift Servers FlexMatch is available (see [service locations](https://docs.aws.amazon.com/gameliftservers/latest/developerguide/gamelift-regions.html))
- Node.js 22 or later. After `npm ci`, bootstrap the AWS CDK once for that account and Region with `npx cdk bootstrap`
- Go 1.25 or later
- Lambda concurrency: `aws lambda get-account-settings` must show `UnreservedConcurrentExecutions` of at least 110, because the stack reserves 10 for the player API function (new accounts can start with a lower limit; request an increase)
- A Bash shell on macOS or Linux (on Windows, use Windows Subsystem for Linux), with `openssl` for `scripts/create-test-player.sh`
- A web browser

## Deploy and play

```bash
npm ci
npx cdk bootstrap                                # once per account and Region
npx cdk deploy                                   # the fleet, matchmaker, queue, and player API (confirm the AWS Identity and Access Management (IAM) changes with y)
./scripts/create-test-player.sh player-one       # prints a password once
./scripts/create-test-player.sh player-two
./scripts/play.sh                                # steps 1 to 7, on your machine
```

`scripts/play.sh` registers your machine as a compute in the Anywhere fleet, keeps a game server process ready (each process hosts one match and then exits, so the script starts the next one), serves the page at http://127.0.0.1:8080/, and deregisters your machine when you press Ctrl+C. Each program listens on `127.0.0.1` only.

1. Open http://127.0.0.1:8080/ in two browser windows side by side. On macOS the script opens the first one for you (set `NO_OPEN=1` to stop that). Keep both windows visible: browsers pause the page's drawing loop in a hidden tab, which also stops it sending moves, and after 15 seconds of silence the server treats that player as gone (during a match, the other player wins by forfeit).
2. Sign in as `player-one` in one window and as `player-two` in the other.
3. Press **Find match** in both. When the match starts, click a window to move its paddle with the up and down arrow keys, or W and S. The first player to reach the target score (7 by default) wins.

To play alone, see [Playing alone](#playing-alone).

The script needs the AWS CLI and Go, and credentials for the stack's account and Region. Both `play.sh` and `create-test-player.sh` read the stack name from `STACK_NAME`: set it if you deployed with `-c stackName=<name>`. Set `COMPUTE_NAME` to change the compute name (default `local-dev-<your user name>`) and `PORT` to change the game server port (default 33430). Port 8080 must be free, so stop any `go run ./cmd/web` from [Run the game without AWS](#run-the-game-without-aws) first. If you set `WEB_PORT` to serve the page on another port, also deploy with `-c allowedOrigins=http://127.0.0.1:<port>`, because the player API accepts calls only from the origins in `allowedOrigins`.

### Playing alone

Press **Find match** in one window and wait. After about 15 seconds you play against a computer player, and you don't run anything extra. This is the behavior a title needs when a player shouldn't wait forever for an opponent. The sample builds it from three pieces: an expansion in the rule set, the player count in the game session's matchmaker data, and a computer player in the game server.

![Flowchart: a ticket enters the matchmaking pool. If a second player's ticket arrives within 15 seconds, FlexMatch forms a two-player match. If not, an expansion lowers the team size from 2 to 1 and FlexMatch forms a one-player match. Either way, the queue places the match on a ready process and a game session is created](matchmaking-flow.png)

The table adds the timing and the code behind each step.

| Time | What happens | Where |
|---|---|---|
| 0 s | Your ticket enters the matchmaking pool. The rule set needs a team of two players, so FlexMatch waits. | [lib/matchmaking.ts](lib/matchmaking.ts) |
| 0 to 15 s | If a second ticket arrives, FlexMatch forms a two-player match within a few seconds. Nothing below happens. | |
| 15 s | No second ticket arrived. An *expansion* in the rule set lowers the team's `minPlayers` from 2 to 1, so your ticket alone completes a match. | [lib/matchmaking.ts](lib/matchmaking.ts) `RULE_SET_BODY` |
| Match start | The queue places the match as usual, with one player session. `OnStartGameSession` delivers the game session, and the server counts the players in the matchmaker data (`humansIn`) and passes the count to the match. | [server/gamelift/process.go](server/gamelift/process.go), [server/gamelift/lifecycle.go](server/gamelift/lifecycle.go) |
| You connect | When you take the first seat, the server seats a computer player in the other one, and the match starts. The computer moves its paddle toward the ball a little slower than a perfect player. | [server/game/match.go](server/game/match.go), [server/game/ai.go](server/game/ai.go) |

Why this design:

- **The rule set decides when to give up waiting, not your code.** A rule set that accepted one player from the start would not wait for an opponent at all. An expansion keeps the better outcome (two people) available for as long as you choose to wait.
- **The backend doesn't change.** The same `StartMatchmaking` call and the same ticket polling serve both kinds of match. Only the wait before the match starts differs.
- **The computer player lives in the game server, and the service doesn't know about it.** The server decides each move anyway, so a computer player is a source of moves like any other. It has no connection and no player session, so at the end the server calls `RemovePlayerSession` only for the one human player, and it works the same on each fleet type.

To change the wait, set `WAIT_FOR_OPPONENT_SECONDS` in [lib/matchmaking.ts](lib/matchmaking.ts) and deploy. A rule set can't be edited, so the stack creates a new rule set named with a hash of its body and switches the matchmaking configuration to it.

### Reading the game server log

While you play, `scripts/play.sh` prints what it set up and then the game server's log. The log lines name the step they belong to, so you can follow a match (`scripts/play.sh` hides the game session ID, and the `time=` and `level=` fields are removed here):

```
  Your machine is now compute local-dev-you in fleet fleet-1234..., location custom-gameserverandmatchmakingstack.
  The service will send players to 127.0.0.1:33430, which is the game server on this machine.

msg="step 1: InitSDK connected this process to Amazon GameLift Servers"
msg="step 1: ProcessReady sent; waiting for the service to start a game session" port=33430
msg="step 6: OnStartGameSession received; port open, ActivateGameSession sent" properties=map[scoreToWin:7] humans=2
msg="step 7: AcceptPlayerSession accepted a player"
msg="player seated" seat=0
msg="step 7: AcceptPlayerSession accepted a player"
msg="player seated" seat=1
msg="match started"
msg="match finished" winner=1 score="[2 7]" reason=completed
msg="match over" winner=1 scores="[2 7]" reason=completed error=<nil>
msg="after the match: RemovePlayerSession sent for each player" players=2
msg="after the match: ProcessEnding sent; this process exits and a new one starts for the next match"
msg="step 1: InitSDK connected this process to Amazon GameLift Servers"
```

Steps 2 to 5 happen in the browser and in your AWS account, so they don't appear in this log. The last line is the next process starting: on an Anywhere fleet `scripts/play.sh` starts it, and on a managed fleet the service does. A match that started with one player shows `humans=1` on the `step 6` line and `no second player: the computer plays seat 1` after `player seated`. A refused join shows as `step 7: AcceptPlayerSession refused a player`, for example when a player session ID is wrong or was already used.

## Teardown

Resources in this sample incur charges while they run. With `fleetType=ec2`, the fleet keeps an Amazon EC2 instance running until you delete the stack, so delete the stack when you finish. Stop `scripts/play.sh` with Ctrl+C, then delete the stack:

```bash
npx cdk destroy
```

If you deployed with `-c stackName=<name>`, run `npx cdk destroy -c stackName=<name>`. Set `STACK_NAME=<name>` for the scripts in the same way.

This deletes the fleet (and, with `fleetType=ec2`, its build), the matchmaker and queue, the player API, the user pool and its players, and the player API's log groups, and it schedules the AWS Key Management Service (AWS KMS) key that encrypts the log groups for deletion after a 7-day waiting period. To keep the log groups and the key, deploy with `-c deleteLogsOnDestroy=false` before you delete the stack; the setting takes effect when you deploy, not when you destroy. The game server's own log is not in a log group: on a managed fleet the service stores it in Amazon Simple Storage Service (Amazon S3) for 14 days after the session ends (deleting the stack doesn't remove it sooner), and on your machine `scripts/play.sh` prints it while it runs and deletes it when it stops. The CDK keeps the uploaded build and function code in its asset bucket in Amazon S3, which `npx cdk destroy` doesn't empty.

## Run the game without AWS

To work on the game itself, run the server in local mode. It accepts any non-empty player session ID and doesn't contact Amazon GameLift Servers.

```bash
cd server
go test ./...
go run ./cmd/server -score 3        # terminal 1: the game server on ws://127.0.0.1:7777
go run ./cmd/web                    # terminal 2: the page on http://127.0.0.1:8080, no sign-in
go run ./cmd/bot                    # terminal 3: an opponent
```

The server runs one match and exits; start it again for the next one. A match needs two players, here the page and the bot, or two browser windows. Local mode has no computer player, and if the second player doesn't join within 2 minutes the server exits without a match.

## From Anywhere to a managed fleet

The game server code doesn't change between fleet types. To run the game server on a managed Amazon EC2 fleet instead of your machine, build it for Linux and deploy with `fleetType=ec2`:

```bash
./scripts/build-server.sh                          # compiles server/build/server for Linux on AWS Graviton (arm64)
npx cdk deploy -c fleetType=ec2 -c allowedCidr=203.0.113.7/32   # waits until the fleet's instance is ready
./scripts/play.sh                                  # now only serves the page
```

Replace `203.0.113.7/32` with the address range of the players who should reach the game server, for example your own public IP address as a `/32`. The setting has no default. The deployment refuses `0.0.0.0/0` (every address) unless you also pass `-c allowPublic=true`, but it doesn't check other broad ranges, so use the narrowest range that works. The stack holds one fleet at a time: deploying with `fleetType=ec2` replaces the Anywhere fleet. To go back, deploy without `fleetType` or `allowedCidr`; the stack refuses `allowedCidr` on the Anywhere fleet.

The first deploy with `fleetType=ec2` takes longer, because `cdk deploy` waits until the fleet's instance is ready and its game server process has reported ready.

What changes on the managed fleet ([lib/ec2-fleet.ts](lib/ec2-fleet.ts)):

- **The service runs the machines.** You upload the build (the CDK app does it from `server/build`), and Amazon GameLift Servers starts one On-Demand instance (billed while it runs, unlike a Spot Instance, which costs less but the service can reclaim), installs the build, starts the game server process with the settings in the fleet's runtime configuration, and starts a new process after each match. The service also gives each process its connection to Amazon GameLift Servers, so the server calls `InitSDKFromEnvironment` and finds the connection settings the service put in its environment, and you register nothing yourself.
- **Players connect over the internet.** The server listens on the instance's network interfaces (`-bind-host 0.0.0.0`), and the fleet's inbound permissions open only the game port, only to `allowedCidr`.
- **The connection is encrypted.** The fleet has TLS certificate generation turned on. When a game session starts, the server loads the certificate with the server SDK's `GetComputeCertificate` (`-tls`) and serves `wss://`. The sample calls it when a game session starts, after `ProcessReady`; in testing, the call failed before `ProcessReady`. Players connect to the game session's DNS name, which matches the certificate. TLS encrypts the connection, which helps protect the packets from being read or changed on the way. The certificate comes from AWS Certificate Manager (ACM) and is valid for a limited time that starts when you create the fleet: up to 200 days for fleets created from March 1, 2026, and up to 100 days for fleets created from March 1, 2027 (see Data protection in the Amazon GameLift Servers documentation). Replace the fleet before the certificate expires.
- **The service keeps the logs.** The game server writes its log to `/local/game/logs`, which it reports in `ProcessReady`. When a game session ends, the service stores those logs in Amazon S3 for 14 days; download them from the Amazon GameLift Servers console or with `GetGameSessionLogUrl`. Log files over the service's size limit aren't saved.

A managed container fleet is much the same from the game server's point of view; you provide a container image instead of a build. This sample doesn't include one.

## How the sample limits access

- **The programs `scripts/play.sh` starts don't receive your AWS credentials.** The script itself and the AWS CLI calls in it use them (register the compute, fetch the auth token). The game server and the page server start with no AWS credentials in their environment, only `PATH`, and the game server also gets the `GAMELIFT_SDK_*` settings it needs, including a short-lived compute auth token that the service issues for this compute only. They still run as your user, so they could read credential files in your home folder; the sample's programs don't.
- **The player API requires a sign-in.** Each route needs a token from the user pool (the page sends its access token); a request without one gets `401 Unauthorized`. The function takes the player ID from the verified token, starts each ticket ID with that player ID, and returns only the caller's own player session ID, so a player can read or cancel only their own tickets. API Gateway checks the token's signature and expiry, not whether it was revoked: after you delete a player, a token already issued keeps working until it expires (1 hour in this sample).
- **The player API is rate limited.** The API stage allows 20 requests per second with bursts of 40, shared by all players, and the function runs at most 10 copies at once. When a check fails (for example, the API is throttled), the page tries again, and after five failures in a row it cancels the search. At one poll every 2 seconds, about 40 players searching at once reach that limit. The browser origins in `allowedOrigins` (cross-origin resource sharing, CORS) keep other web pages from calling the API in a browser, but they don't stop other clients that hold a valid token.
- **Players can't sign themselves up.** Self sign-up is turned off, so players exist only when an administrator creates them, for example with `scripts/create-test-player.sh`.
- **The game server checks each player.** In `-gamelift` mode the server lets a player in only after `AcceptPlayerSession` accepts their player session ID, and one player session ID can hold only one seat. A web page can connect only if its origin is in the server's `-allow-origin` list. Clients that aren't browsers send no origin and pass that check; the player session ID check is what controls access.
- **Clients can't change the game.** A client sends only a move from -1 (up) to 1 (down), and the server limits any other number to that range. Where the ball is, whether it hits a paddle, and the score are decided on the server.
- **The function's permissions are narrow where the service allows it.** Its role can write only to its own log group. The Service Authorization Reference lists no resource type for `StartMatchmaking`, `DescribeMatchmaking`, or `StopMatchmaking`, and a policy that scoped `StartMatchmaking` to the matchmaking configuration's Amazon Resource Name (ARN) was denied when the function called it, so the role allows these three actions with a resource of `*`, limited to the stack's Region. The function uses only the matchmaking configuration named in its environment.

## Settings

| Context key | Default | Description |
|---|---|---|
| `stackName` | `GameServerAndMatchmakingStack` | Name of the AWS CloudFormation stack. Use a different name to deploy a second copy in the same account |
| `scoreToWin` | `7` | Points that end a match (1 to 20). The matchmaker passes it to the server as a game property |
| `allowedOrigins` | `http://127.0.0.1:8080,http://localhost:8080` | Browser origins allowed to call the player API, separated by commas |
| `logRetentionDays` | `30` | Days to keep the player API's logs. Amazon CloudWatch Logs accepts only certain values, such as 1, 7, 14, 30, 90, or 365 |
| `deleteLogsOnDestroy` | `true` | Delete the player API's log groups when you delete the stack |
| `fleetType` | `anywhere` | `anywhere` runs the game server on your machine; `ec2` runs it on a managed Amazon EC2 fleet |
| `allowedCidr` | none | With `fleetType=ec2`, the only Internet Protocol version 4 (IPv4) range that can reach the game port, for example `203.0.113.7/32`. Required for `ec2` |
| `allowPublic` | `false` | With `fleetType=ec2`, set to `true` only if you intend `allowedCidr=0.0.0.0/0` (not used for other ranges, which the stack accepts as given), which opens the game port to the internet |

Pass a setting with `-c`, for example `npx cdk deploy -c scoreToWin=3`. CDK doesn't remember settings between deploys, so pass every setting you use each time. A deploy without `-c fleetType=ec2` switches the stack back to the Anywhere fleet.

## Limitations

- **One match at a time on your machine.** `scripts/play.sh` runs one game server process on one port. A production compute usually runs several processes on different ports.
- **You create the players.** The user pool has self sign-up turned off and no email or phone. A production title signs players in with its platform identity and exchanges that identity for a token; this sample keeps that step out of scope.
- **Matchmaking status is polled every 2 seconds.** The page asks the player API for its ticket every 2 seconds so a demo match starts quickly, and the function calls `DescribeMatchmaking`. The FlexMatch guide (Track matchmaking events) recommends polling no more than once every 10 seconds, starting at least 30 seconds after the ticket is submitted, and only for low-volume development; games in public release use event notifications. For production traffic, set `notificationTarget` on the matchmaking configuration to an Amazon Simple Notification Service (Amazon SNS) topic, store the events FlexMatch sends, and read from that store instead.
- **WebSocket instead of UDP.** The game uses WebSocket so a browser can connect to the server directly. Many real-time game servers send their gameplay over UDP; the Amazon GameLift Servers part of the code is the same either way.
- **The local connection isn't encrypted.** On the Anywhere fleet the page connects without encryption to `127.0.0.1`, so traffic doesn't leave your machine. The EC2 fleet uses encrypted WebSocket (`wss`), and the page refuses to connect without encryption to any address other than `127.0.0.1`.
- **`allowPublic` opens the game port to the internet.** Leave it off unless you have a reason; a player session ID is still required to take a seat.
- **Some parts are the sample's own, not service features.** They are the demo game and its computer player, the one-search-per-minute guard in the player API, the connection limits and origin check on the game port, the 30-second limit for a match's players to join, and the health check that reports a stuck match.
- **The client draws what the server sends.** It doesn't predict its own paddle or smooth motion between updates, which a production client does to hide network delay.
- **The game is a demo workload.** Two players, simple rules, and no reconnect after a dropped connection.
- **AWS WAF and a custom domain aren't set up.** The player API uses the default Amazon API Gateway address. A custom domain name works on this HTTP API as it is. AWS WAF can't attach to an HTTP API directly: to add it, give the API a custom domain name, put an Amazon CloudFront distribution in front of that domain, attach AWS WAF to the distribution, and then turn off the API's default endpoint so clients can't go around CloudFront (the default endpoint can be turned off only when the API answers on a custom domain), or switch the player API to a REST API. Separately, you can attach AWS WAF to the Amazon Cognito user pool to filter sign-in requests; that doesn't cover the player API. Set up AWS WAF before you open the player API to many people.
- **The game port has only simple connection limits.** It accepts at most 4 connections from one address and 32 in total, and closes a connection that doesn't send its join message within 5 seconds ([server/game/limits.go](server/game/limits.go), [server/game/match.go](server/game/match.go)). That keeps one address from using every slot, but a few addresses together can still fill them, so keep `allowedCidr` narrow, and for a UDP game consider player gateway.
- **A player gets one match search a minute.** The function refuses a new search while the player's last one is still running, and each ticket ID is the player ID plus the current minute ([lambda/matchmaking/index.mjs](lambda/matchmaking/index.mjs)), so a player can start one search per minute. After a cancel, a failed search, or a short match, the player may wait up to a minute to search again. That keeps one player from holding the only game server process with several searches, but it is not a quota: a production title limits searches in its backend with a store of its own.
- **The computer player is simple.** It chases the ball and doesn't predict. A lone player isn't joined by a late opponent, because the sample doesn't use FlexMatch backfill; a production title can enable backfill so a waiting player joins a running match.
- **The EC2 fleet is set up for one instance.** It runs one instance with one game server process and has no scaling policy. Amazon GameLift Servers can scale a fleet, and for production traffic you add a scaling policy and more processes per instance. On either fleet, with one process, a player who searches while a match is running waits about a minute (up to 15 seconds longer if the player searches alone, because FlexMatch first waits for an opponent), and then the search ends with "No game server was ready in time" or "Matchmaking ended: FAILED". A match whose players never connect holds the process for up to 30 seconds.

## Security

See [CONTRIBUTING](CONTRIBUTING.md#security-issue-notifications) for more information.

## License

This sample is licensed under the MIT-0 License. See the [LICENSE](LICENSE) file.
