import * as crypto from 'node:crypto';
import * as cdk from 'aws-cdk-lib/core';
import * as gamelift from 'aws-cdk-lib/aws-gamelift';
import { Construct } from 'constructs';

export interface MatchmakingProps {
  /** Prefix for resource names, for example the stack name. */
  readonly namePrefix: string;
  /** Fleets the queue can place game sessions on. */
  readonly fleetArns: string[];
  /** Goals needed to win; passed to the server as the `scoreToWin` game property. */
  readonly scoreToWin: number;
}

/**
 * Seconds a matchmaking ticket may stay in the pool before it times out. This is the limit for the whole search,
 * and it is longer than the wait for a second player (WAIT_FOR_OPPONENT_SECONDS), which the rule set sets.
 */
const TICKET_TIMEOUT_SECONDS = 60;

/**
 * Seconds FlexMatch waits for a second player before it lets a match start with one. Raise it to give players
 * more time to find a real opponent; lower it to start a match against the computer sooner.
 */
export const WAIT_FOR_OPPONENT_SECONDS = 15;

/**
 * The FlexMatch rule set: one team that needs two players. This is the smallest useful rule set. Production titles add
 * rules such as similar skill, or acceptable latency to the location that will host the match.
 *
 * How a lone player gets a match, in order:
 * 1. The player's ticket enters the matchmaking pool. The rule set needs a team of two, so FlexMatch waits.
 * 2. If another ticket arrives, FlexMatch forms a two-player match at once and the expansion never starts.
 * 3. If not, the expansion starts when the newest ticket in the incomplete match has waited
 *    WAIT_FOR_OPPONENT_SECONDS. It lowers the team's minPlayers from 2 to 1, so the lone ticket now completes
 *    a match. (The rule set must lower minPlayers rather than start at 1: a rule set that accepts one player
 *    from the start would never wait for an opponent.)
 * 4. The queue places the match as usual. The game session has one player session, and its matchmaker data
 *    lists one player. The game server reads that (humansIn in server/gamelift/process.go) and plays the empty
 *    seat itself (server/game/ai.go).
 *
 * Nothing else in the backend changes: the same StartMatchmaking call and the same ticket polling serve both
 * kinds of match. The only visible difference to the player is a longer wait before the match starts.
 *
 * A rule set can't be edited after it is created. A changed body is a new rule set, so the name below carries a
 * hash of the body and the matchmaking configuration switches to the new one when you deploy.
 */
const RULE_SET_BODY = JSON.stringify({
  name: 'two-player-match',
  ruleLanguageVersion: '1.0',
  teams: [{ name: 'players', minPlayers: 2, maxPlayers: 2 }],
  rules: [],
  expansions: [{
    target: 'teams[players].minPlayers',
    steps: [{ waitTimeSeconds: WAIT_FOR_OPPONENT_SECONDS, value: 1 }],
  }],
});

/**
 * Deployed before the steps. At run time this is step 5: the matchmaker and the queue.
 *
 * - The matchmaking configuration (the matchmaker) groups tickets from StartMatchmaking into matches using
 *   the rule set.
 * - The game session queue receives each finished match and picks a fleet location with a ready server
 *   process to host it. With several fleets or locations, the queue is where you set the order of preference.
 * - This sample has no notification target. Its player API polls the ticket instead. A backend for many players
 *   should set notificationTarget on the matchmaking configuration to an Amazon Simple Notification Service
 *   (Amazon SNS) topic, store the events FlexMatch sends for every ticket change, and read from that store.
 */
export class Matchmaking extends Construct {
  public readonly configuration: gamelift.CfnMatchmakingConfiguration;
  public readonly queue: gamelift.CfnGameSessionQueue;

  constructor(scope: Construct, id: string, props: MatchmakingProps) {
    super(scope, id);
    const configurationName = `${props.namePrefix}-matchmaker`;

    const ruleSet = new gamelift.CfnMatchmakingRuleSet(this, 'RuleSet', {
      // A rule set can't be edited, so its name carries a hash of its body: changing the rules makes a new rule
      // set next to the old one, and the matchmaking configuration switches to it.
      name: `${props.namePrefix}-rules-${crypto.createHash('sha256').update(RULE_SET_BODY).digest('hex').slice(0, 8)}`,
      ruleSetBody: RULE_SET_BODY,
    });

    this.queue = new gamelift.CfnGameSessionQueue(this, 'Queue', {
      name: `${props.namePrefix}-queue`,
      timeoutInSeconds: 60,
      destinations: props.fleetArns.map((destinationArn) => ({ destinationArn })),
    });

    // FlexMatch WITH_QUEUE: when a match is formed, FlexMatch asks the queue to start a game session for it.
    this.configuration = new gamelift.CfnMatchmakingConfiguration(this, 'Configuration', {
      name: configurationName,
      ruleSetName: ruleSet.name,
      acceptanceRequired: false,
      requestTimeoutSeconds: TICKET_TIMEOUT_SECONDS,
      flexMatchMode: 'WITH_QUEUE',
      gameSessionQueueArns: [this.queue.attrArn],
      // No backfill: a match is the players FlexMatch put together (two, or one plus the computer player).
      backfillMode: 'MANUAL',
      // Game properties reach the server in OnStartGameSession (server/gamelift/lifecycle.go).
      gameProperties: [{ key: 'scoreToWin', value: String(props.scoreToWin) }],
    });
    this.configuration.addResourceDependency(ruleSet);

    new cdk.CfnOutput(this, 'MatchmakingConfigurationName', { value: configurationName });
  }
}
