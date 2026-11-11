import * as fs from 'node:fs';
import * as path from 'node:path';
import * as cdk from 'aws-cdk-lib/core';
import * as gamelift from 'aws-cdk-lib/aws-gamelift';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as s3assets from 'aws-cdk-lib/aws-s3-assets';
import { Construct } from 'constructs';

export const GAME_PORT = 33430;
/** An AWS Graviton instance type. The build is compiled for arm64 to match (scripts/build-server.sh). */
const INSTANCE_TYPE = 'c7g.large';
export const SERVER_BUILD_DIR = path.join(__dirname, '..', 'server', 'build');

export interface Ec2FleetProps {
  readonly fleetName: string;
  /** The only address range that can reach the game server port, for example your own IP address as a /32. */
  readonly allowedCidr: string;
  /** Web page origins the game server accepts connections from. */
  readonly allowedOrigins: string[];
  /** Folder that holds the compiled server. Default: server/build. */
  readonly buildDir?: string;
}

/**
 * Deployed before the steps, as an alternative to the Anywhere fleet: a managed Amazon EC2 fleet. Amazon
 * GameLift Servers starts the instance, installs the build, starts the game server process, and restarts it after
 * every match. The game server code is the same one that runs on your machine on the Anywhere fleet.
 *
 * Players reach the instance over the internet, so three things change compared with Anywhere:
 * - the fleet's inbound permissions open the game port to one address range only;
 * - the fleet generates a TLS certificate, and the server serves wss:// with it (players connect to the game
 *   session's DNS name, which matches the certificate);
 * - the server listens on all interfaces of the instance (-bind-host 0.0.0.0).
 */
export class Ec2Fleet extends Construct {
  public readonly fleet: gamelift.CfnFleet;

  constructor(scope: Construct, id: string, props: Ec2FleetProps) {
    super(scope, id);
    const stack = cdk.Stack.of(this);

    // The build is the compiled game server (scripts/build-server.sh puts it in server/build). Amazon GameLift
    // Servers copies it from Amazon S3 with a role you give it.
    const buildDir = props.buildDir ?? SERVER_BUILD_DIR;
    if (!fs.existsSync(path.join(buildDir, 'server'))) {
      throw new Error('No game server build found. Run ./scripts/build-server.sh before deploying with fleetType=ec2.');
    }
    const asset = new s3assets.Asset(this, 'ServerBuild', { path: buildDir });
    const buildRole = new iam.Role(this, 'BuildAccessRole', {
      // The trust policy follows the documented form for build roles. The role can read one file only.
      assumedBy: new iam.ServicePrincipal('gamelift.amazonaws.com'),
      description: 'Lets Amazon GameLift Servers read the game server build from Amazon S3',
    });
    // Read access to this one build file only.
    buildRole.addToPolicy(new iam.PolicyStatement({
      actions: ['s3:GetObject', 's3:GetObjectVersion'],
      resources: [asset.bucket.arnForObjects(asset.s3ObjectKey)],
    }));
    const build = new gamelift.CfnBuild(this, 'Build', {
      name: `${props.fleetName}-build`,
      operatingSystem: 'AMAZON_LINUX_2023',
      serverSdkVersion: '5.6.0',
      storageLocation: { bucket: asset.s3BucketName, key: asset.s3ObjectKey, roleArn: buildRole.roleArn },
    });
    build.node.addDependency(buildRole);

    this.fleet = new gamelift.CfnFleet(this, 'Fleet', {
      name: props.fleetName,
      description: 'Managed EC2 fleet for the game server',
      computeType: 'EC2',
      buildId: build.ref,
      ec2InstanceType: INSTANCE_TYPE,
      fleetType: 'ON_DEMAND',
      // Only this address range can reach the game port. No other port is open (no SSH or remote desktop).
      // The demo game uses WebSocket, which runs over TCP. For a game that sends UDP, change the protocol to 'UDP'.
      ec2InboundPermissions: [{ fromPort: GAME_PORT, toPort: GAME_PORT, ipRange: props.allowedCidr, protocol: 'TCP' }],
      certificateConfiguration: { certificateType: 'GENERATED' },
      runtimeConfiguration: {
        serverProcesses: [{
          launchPath: '/local/game/server',
          parameters: `-gamelift -tls -port ${GAME_PORT} -bind-host 0.0.0.0 -allow-origin ${props.allowedOrigins.join(',')}`,
          concurrentExecutions: 1,
        }],
      },
      newGameSessionProtectionPolicy: 'FullProtection', // don't scale in an instance in the middle of a match
      locations: [{ location: stack.region, locationCapacity: { desiredEc2Instances: 1, minSize: 0, maxSize: 1 } }],
    });

    new cdk.CfnOutput(this, 'FleetId', { value: this.fleet.attrFleetId });
  }
}
