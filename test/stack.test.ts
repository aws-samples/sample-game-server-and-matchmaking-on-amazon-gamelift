import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import * as cdk from 'aws-cdk-lib/core';
import { Match, Template } from 'aws-cdk-lib/assertions';
import { RealtimeServerStack } from '../lib/realtime-server-stack';

const env = { account: '111111111111', region: 'us-east-1' };
const synth = (props: ConstructorParameters<typeof RealtimeServerStack>[2] = {}) =>
  Template.fromStack(new RealtimeServerStack(new cdk.App(), 'Test', { env, ...props }));

function ec2Template() {
  const buildDir = fs.mkdtempSync(path.join(os.tmpdir(), 'build-'));
  fs.writeFileSync(path.join(buildDir, 'server'), 'placeholder');
  return synth({ fleetType: 'ec2', allowedCidr: '203.0.113.7/32', serverBuildDir: buildDir });
}

describe('what the README teaches', () => {
  test.each([['Anywhere', synth], ['EC2', ec2Template]])('the %s stack has the outputs the scripts read', (_name, make) => {
    // scripts/play.sh and scripts/create-test-player.sh find outputs by a substring of the output key.
    const keys = Object.keys((make() as Template).toJSON().Outputs ?? {});
    for (const part of ['FleetId', 'FleetType', 'ApiUrl', 'UserPoolClientId', 'UserPoolId']) {
      expect(keys.filter((k) => k.includes(part))).not.toHaveLength(0);
    }
  });

  test('the rule set needs two players and relaxes to one after 15 seconds', () => {
    const rules = Object.values(synth().findResources('AWS::GameLift::MatchmakingRuleSet'));
    expect(rules).toHaveLength(1);
    const body = JSON.parse(rules[0].Properties.RuleSetBody);
    expect(body.teams).toEqual([{ name: 'players', minPlayers: 2, maxPlayers: 2 }]);
    expect(body.expansions).toEqual([{ target: 'teams[players].minPlayers', steps: [{ waitTimeSeconds: 15, value: 1 }] }]);
  });

  test('the matchmaker places matches through the queue and passes scoreToWin to the server', () => {
    synth({ scoreToWin: 3 }).hasResourceProperties('AWS::GameLift::MatchmakingConfiguration', {
      FlexMatchMode: 'WITH_QUEUE',
      BackfillMode: 'MANUAL',
      AcceptanceRequired: false,
      GameProperties: [{ Key: 'scoreToWin', Value: '3' }],
    });
  });

  test('the Anywhere fleet opens no port', () => {
    const template = synth();
    template.resourceCountIs('AWS::GameLift::Fleet', 1);
    template.hasResourceProperties('AWS::GameLift::Fleet', { ComputeType: 'ANYWHERE' });
    expect(Object.values(template.findResources('AWS::GameLift::Fleet'))[0].Properties.EC2InboundPermissions).toBeUndefined();
  });

  test('the EC2 fleet opens one TCP port to the allowed range only, with TLS certificates', () => {
    ec2Template().hasResourceProperties('AWS::GameLift::Fleet', {
      ComputeType: 'EC2',
      FleetType: 'ON_DEMAND',
      EC2InboundPermissions: [{ FromPort: 33430, ToPort: 33430, IpRange: '203.0.113.7/32', Protocol: 'TCP' }],
      CertificateConfiguration: { CertificateType: 'GENERATED' },
    });
  });

  test('no template opens a port to the whole internet', () => {
    for (const template of [synth(), ec2Template()]) {
      expect(JSON.stringify(template.toJSON())).not.toMatch(/0\.0\.0\.0\/0|::\/0/);
    }
  });

  test('the player API log groups are encrypted with a key that rotates', () => {
    const template = synth();
    const groups = Object.values(template.findResources('AWS::Logs::LogGroup'));
    expect(groups).toHaveLength(2);
    for (const group of groups) expect(group.Properties.KmsKeyId).toBeDefined();
    template.hasResourceProperties('AWS::KMS::Key', { EnableKeyRotation: true });
  });

  test('the matchmaking permissions are limited to the stack Region', () => {
    const policies = Object.values(synth().findResources('AWS::IAM::Policy'));
    const statements = policies.flatMap((p) => p.Properties.PolicyDocument.Statement as any[]);
    const matchmaking = statements.filter((st) => JSON.stringify(st.Action).includes('gamelift:StartMatchmaking'));
    expect(matchmaking).toHaveLength(1);
    expect(matchmaking[0].Condition).toEqual({ StringEquals: { 'aws:RequestedRegion': 'us-east-1' } });
  });

  test('the function carries the cfn-guard suppression for the rules that do not apply', () => {
    const fns = Object.values(synth().findResources('AWS::Lambda::Function'));
    expect(fns.filter((f) => f.Metadata?.guard?.SuppressedRules?.includes('LAMBDA_DLQ_CHECK'))).toHaveLength(1);
  });

  test('the player API requires a token on every route', () => {
    const routes = Object.values(synth().findResources('AWS::ApiGatewayV2::Route'));
    expect(routes.length).toBeGreaterThan(0);
    for (const route of routes) expect(route.Properties.AuthorizationType).toBe('JWT');
  });
});
