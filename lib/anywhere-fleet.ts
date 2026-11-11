import * as cdk from 'aws-cdk-lib/core';
import * as gamelift from 'aws-cdk-lib/aws-gamelift';
import { Construct } from 'constructs';

export interface AnywhereFleetProps {
  /** Custom location name. Must start with `custom-`. */
  readonly locationName: string;
  readonly fleetName: string;
}

/**
 * Deployed before the steps: where game servers run. An Amazon GameLift Servers fleet is a set of compute in one or more
 * locations. This is an Anywhere fleet: you register your own compute (here, your laptop) and run the game
 * server process on it yourself, so a match runs end to end against the real service without uploading a
 * build or opening any inbound port. A managed Amazon EC2 fleet uses the same server code; the service then
 * starts the processes for you from an uploaded build.
 *
 * A custom location is a name for where Anywhere compute lives, for example a data center. Each compute you
 * register says which location it is in.
 */
export class AnywhereFleet extends Construct {
  public readonly fleet: gamelift.CfnFleet;
  public readonly locationName: string;

  constructor(scope: Construct, id: string, props: AnywhereFleetProps) {
    super(scope, id);
    this.locationName = props.locationName;

    const location = new gamelift.CfnLocation(this, 'Location', { locationName: props.locationName });
    this.fleet = new gamelift.CfnFleet(this, 'Fleet', {
      name: props.fleetName,
      description: 'Anywhere fleet for running the game server on your own compute',
      computeType: 'ANYWHERE',
      locations: [{ location: props.locationName }],
    });
    this.fleet.addResourceDependency(location);

    new cdk.CfnOutput(this, 'FleetId', { value: this.fleet.attrFleetId });
    new cdk.CfnOutput(this, 'LocationName', { value: props.locationName });
  }
}
