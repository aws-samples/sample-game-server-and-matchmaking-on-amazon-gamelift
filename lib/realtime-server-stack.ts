import * as cdk from 'aws-cdk-lib/core';
import * as logs from 'aws-cdk-lib/aws-logs';
import type { Construct } from 'constructs';
import { AnywhereFleet } from './anywhere-fleet';
import { Ec2Fleet } from './ec2-fleet';
import { Matchmaking } from './matchmaking';
import { PlayerApi } from './player-api';

export const DEFAULT_SCORE_TO_WIN = 7;
/** Where the local browser client is served (see server/cmd/web). */
export const DEFAULT_ALLOWED_ORIGINS = ['http://127.0.0.1:8080', 'http://localhost:8080'];

export interface RealtimeServerStackProps extends cdk.StackProps {
  /** Days to keep the player API's logs (function and access logs). Must be a value CloudWatch Logs accepts. Default: 30. */
  readonly logRetentionDays?: number;
  /** Delete the player API's log groups on `cdk destroy`. Default: true (self-contained demo). */
  readonly deleteLogsOnDestroy?: boolean;
  /** Goals needed to win a match. Default: 7. */
  readonly scoreToWin?: number;
  /** Browser origins allowed to call the player API. Default: the local browser client on port 8080. */
  readonly allowedOrigins?: string[];
  /** Where game servers run: your own machine (anywhere) or a managed Amazon EC2 fleet (ec2). Default: anywhere. */
  readonly fleetType?: 'anywhere' | 'ec2';
  /** For ec2: the only address range that can reach the game port. */
  readonly allowedCidr?: string;
  /** For ec2: folder with the compiled server. Default: server/build (see scripts/build-server.sh). */
  readonly serverBuildDir?: string;
}

/**
 * Everything that is deployed before the seven steps (see server/gamelift/doc.go):
 * - AnywhereFleet (default) or Ec2Fleet (-c fleetType=ec2): where game servers run.
 * - Matchmaking: the FlexMatch matchmaker, its rule set, and the queue that places matches on the fleet.
 * - PlayerApi: player sign-in and the API that calls the matchmaker.
 */
export class RealtimeServerStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props: RealtimeServerStackProps = {}) {
    super(scope, id, props);

    const logRetention = props.logRetentionDays ?? logs.RetentionDays.ONE_MONTH;
    const logRemovalPolicy = props.deleteLogsOnDestroy === false ? cdk.RemovalPolicy.RETAIN : cdk.RemovalPolicy.DESTROY;

    // Resource names are derived from the stack name, which is validated to be short and URL safe.
    const allowedOrigins = props.allowedOrigins ?? DEFAULT_ALLOWED_ORIGINS;
    let fleetArn: string;
    if (props.fleetType === 'ec2') {
      if (!props.allowedCidr) throw new Error('fleetType=ec2 needs allowedCidr.');
      fleetArn = new Ec2Fleet(this, 'Ec2Fleet', {
        fleetName: `${id}-ec2`, allowedCidr: props.allowedCidr, allowedOrigins, buildDir: props.serverBuildDir,
      }).fleet.attrFleetArn;
    } else {
      fleetArn = new AnywhereFleet(this, 'AnywhereFleet', { locationName: `custom-${id.toLowerCase()}`, fleetName: `${id}-anywhere` }).fleet.attrFleetArn;
    }
    new cdk.CfnOutput(this, 'FleetType', { value: props.fleetType ?? 'anywhere' });

    const matchmaking = new Matchmaking(this, 'Matchmaking', {
      namePrefix: id,
      fleetArns: [fleetArn],
      scoreToWin: props.scoreToWin ?? DEFAULT_SCORE_TO_WIN,
    });

    new PlayerApi(this, 'PlayerApi', {
      matchmakingConfigurationName: matchmaking.configuration.name,
      allowedOrigins,
      logRetention,
      logRemovalPolicy,
    });
  }
}
