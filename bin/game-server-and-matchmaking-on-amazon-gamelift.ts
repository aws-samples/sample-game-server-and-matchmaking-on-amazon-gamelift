#!/usr/bin/env node
import * as cdk from 'aws-cdk-lib/core';
import { AwsSolutionsChecks } from 'cdk-nag';
import { parseContext } from '../lib/context';
import { RealtimeServerStack } from '../lib/realtime-server-stack';

const app = new cdk.App();
// cdk-nag checks every synth; accepted findings are acknowledged in code with a reason.
cdk.Validations.of(app).addPlugins(new AwsSolutionsChecks(app, { verbose: true }));
const { stackName, props } = parseContext((key) => app.node.tryGetContext(key));

new RealtimeServerStack(app, stackName, {
  env: { account: process.env.CDK_DEFAULT_ACCOUNT, region: process.env.CDK_DEFAULT_REGION },
  ...props,
});
