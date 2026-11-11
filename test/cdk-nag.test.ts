import * as cdk from 'aws-cdk-lib/core';
import { Annotations, Match } from 'aws-cdk-lib/assertions';
import { AwsSolutionsChecks } from 'cdk-nag';
import { RealtimeServerStack } from '../lib/realtime-server-stack';

import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

function nagFindings(props: ConstructorParameters<typeof RealtimeServerStack>[2]) {
  const app = new cdk.App();
  const stack = new RealtimeServerStack(app, 'NagTest', { env: { account: '111111111111', region: 'us-east-1' }, ...props });
  cdk.Validations.of(app).addPlugins(new AwsSolutionsChecks(app, { verbose: true }));
  app.synth();
  const a = Annotations.fromStack(stack);
  return [...a.findError('*', Match.stringLikeRegexp('AwsSolutions-.*')), ...a.findWarning('*', Match.stringLikeRegexp('AwsSolutions-.*'))];
}

test('cdk-nag: the EC2 fleet variant has no unacknowledged findings', () => {
  const buildDir = fs.mkdtempSync(path.join(os.tmpdir(), 'build-'));
  fs.writeFileSync(path.join(buildDir, 'server'), 'placeholder');
  expect(nagFindings({ fleetType: 'ec2', allowedCidr: '203.0.113.7/32', serverBuildDir: buildDir })).toEqual([]);
});

test('cdk-nag: the default (Anywhere) stack has no unacknowledged findings', () => {
  expect(nagFindings({})).toEqual([]);
});
