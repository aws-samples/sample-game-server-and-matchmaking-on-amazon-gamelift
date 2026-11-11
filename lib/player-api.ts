import * as path from 'node:path';
import * as cdk from 'aws-cdk-lib/core';
import * as apigw from 'aws-cdk-lib/aws-apigatewayv2';
import { HttpUserPoolAuthorizer } from 'aws-cdk-lib/aws-apigatewayv2-authorizers';
import { HttpLambdaIntegration } from 'aws-cdk-lib/aws-apigatewayv2-integrations';
import * as cognito from 'aws-cdk-lib/aws-cognito';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as kms from 'aws-cdk-lib/aws-kms';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as logs from 'aws-cdk-lib/aws-logs';
import { Construct } from 'constructs';

export interface PlayerApiProps {
  /** Name of the FlexMatch matchmaking configuration players join. */
  readonly matchmakingConfigurationName: string;
  /** Browser origins allowed to call the API, for example `http://127.0.0.1:8080`. */
  readonly allowedOrigins: string[];
  readonly logRetention: logs.RetentionDays;
  readonly logRemovalPolicy: cdk.RemovalPolicy;
}

/**
 * Steps 2 to 5: the backend players talk to. An Amazon Cognito user pool signs players in, and a small HTTP API with
 * one AWS Lambda function starts, checks, and cancels matchmaking for the signed-in player (see
 * lambda/matchmaking/index.mjs). Every route requires a valid token from the user pool.
 *
 * Game clients should never hold AWS credentials that can call Amazon GameLift Servers. Putting the calls
 * behind your own API lets you decide who may queue, and with which player ID.
 */
export class PlayerApi extends Construct {
  public readonly userPool: cognito.UserPool;
  public readonly userPoolClient: cognito.UserPoolClient;
  public readonly api: apigw.HttpApi;

  constructor(scope: Construct, id: string, props: PlayerApiProps) {
    super(scope, id);
    const stack = cdk.Stack.of(this);

    // Players are created by the operator (see scripts/create-test-player.sh). A production title replaces this
    // with its platform sign-in and exchanges that identity for a token here.
    this.userPool = new cognito.UserPool(this, 'Players', {
      selfSignUpEnabled: false,
      signInAliases: { username: true },
      passwordPolicy: {
        minLength: 12,
        requireDigits: true,
        requireLowercase: true,
        requireUppercase: true,
        requireSymbols: true,
      },
      accountRecovery: cognito.AccountRecovery.NONE,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
    });
    cdk.Validations.of(this.userPool).acknowledge({
      id: 'AwsSolutions-COG2',
      reason: 'Demo players are created by the operator for local testing and have no email or phone, so there is no second factor to enroll. A production title signs players in through its platform identity instead of this pool.',
    });
    cdk.Validations.of(this.userPool).acknowledge({
      id: 'AwsSolutions-COG3',
      reason: 'Threat protection is a paid feature plan; the pool has self sign-up off and holds only operator-created demo players. Turn it on if you open sign-up to the public.',
    });
    cdk.Validations.of(this.userPool).acknowledge({
      id: 'AwsSolutions::AwsSolutions-COG8',
      reason: 'The Plus feature plan adds threat protection, which this demo pool (self sign-up off, operator-created players only) does not need. Choose Plus if you open sign-up to the public.',
    });

    // A public client (a browser page can't keep a secret) that signs in with a username and password.
    this.userPoolClient = this.userPool.addClient('WebClient', {
      authFlows: { userPassword: true },
      generateSecret: false,
      preventUserExistenceErrors: true,
      accessTokenValidity: cdk.Duration.hours(1),
      idTokenValidity: cdk.Duration.hours(1),
      refreshTokenValidity: cdk.Duration.hours(8),
    });

    // One customer managed key encrypts both log groups. The key follows the log groups' removal policy: with
    // `deleteLogsOnDestroy=false` the logs stay and so does the key that can read them. With the default, the key
    // is scheduled for deletion (the shortest wait is 7 days) when you delete the stack.
    const logKey = new kms.Key(this, 'LogsKey', {
      description: 'Encrypts the player API log groups',
      enableKeyRotation: true,
      pendingWindow: cdk.Duration.days(7),
      removalPolicy: props.logRemovalPolicy,
    });
    logKey.addToResourcePolicy(new iam.PolicyStatement({
      principals: [new iam.ServicePrincipal(`logs.${stack.region}.amazonaws.com`)],
      actions: ['kms:Encrypt*', 'kms:Decrypt*', 'kms:ReEncrypt*', 'kms:GenerateDataKey*', 'kms:Describe*'],
      resources: ['*'],
      conditions: {
        StringLike: { 'kms:EncryptionContext:aws:logs:arn': `arn:${stack.partition}:logs:${stack.region}:${stack.account}:log-group:*` },
      },
    }));
    const fnLogs = new logs.LogGroup(this, 'MatchmakingFunctionLogs', {
      encryptionKey: logKey,
      retention: props.logRetention,
      removalPolicy: props.logRemovalPolicy,
    });
    const role = new iam.Role(this, 'MatchmakingFunctionRole', {
      assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
      description: 'Starts, checks, and cancels matchmaking for one matchmaking configuration',
    });
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['logs:CreateLogStream', 'logs:PutLogEvents'],
      resources: [`${fnLogs.logGroupArn}:*`],
    }));
    // These three actions have no resource type in the IAM service reference (the AWS Service Authorization
    // Reference lists none for them), so a policy scoped to the matchmaking configuration's ARN is denied at run
    // time and the resource must be *. What a policy can still do is limit the Region. The function only uses the
    // one configuration named in its environment, and only passes ticket IDs that start with the caller's own
    // player ID.
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['gamelift:StartMatchmaking', 'gamelift:DescribeMatchmaking', 'gamelift:StopMatchmaking'],
      resources: ['*'],
      conditions: { StringEquals: { 'aws:RequestedRegion': stack.region } },
    }));
    cdk.Validations.of(role).acknowledge({
      id: 'AwsSolutions::AwsSolutions-IAM5[Resource::*]',
      reason: 'gamelift:StartMatchmaking, gamelift:DescribeMatchmaking, and gamelift:StopMatchmaking have no resource type in the IAM service reference, so the resource must be *; the policy is limited to this stack\'s Region, the function uses only its configured matchmaking configuration, and it checks that each ticket belongs to the caller.',
    });
    cdk.Validations.of(role).acknowledge({
      id: `AwsSolutions::AwsSolutions-IAM5[Resource::<${stack.getLogicalId(fnLogs.node.defaultChild as cdk.CfnElement)}.Arn>:*]`,
      reason: 'Log streams are created at run time with generated names; the wildcard covers streams in this function\'s own log group only.',
    });

    const fn = new lambda.Function(this, 'MatchmakingFunction', {
      runtime: lambda.Runtime.NODEJS_24_X,
      architecture: lambda.Architecture.ARM_64,
      handler: 'index.handler',
      code: lambda.Code.fromAsset(path.join(__dirname, '..', 'lambda', 'matchmaking')),
      role,
      logGroup: fnLogs,
      timeout: cdk.Duration.seconds(10),
      memorySize: 256,
      reservedConcurrentExecutions: 10,
      environment: { MATCHMAKING_CONFIGURATION_NAME: props.matchmakingConfigurationName },
    });
    // API Gateway invokes this function synchronously and the caller gets the error, so a dead-letter queue (which
    // applies to asynchronous invocation) would capture nothing, and a VPC would need a NAT gateway or endpoint to
    // reach the public Amazon GameLift Servers API. The cfn-guard rules support this suppression.
    (fn.node.defaultChild as cdk.CfnResource).addMetadata('guard', { SuppressedRules: ['LAMBDA_DLQ_CHECK', 'LAMBDA_INSIDE_VPC'] });

    this.api = new apigw.HttpApi(this, 'Api', {
      description: 'Matchmaking for signed-in players',
      createDefaultStage: false,
      corsPreflight: {
        allowOrigins: props.allowedOrigins,
        allowMethods: [apigw.CorsHttpMethod.GET, apigw.CorsHttpMethod.POST, apigw.CorsHttpMethod.DELETE],
        allowHeaders: ['authorization', 'content-type'],
        maxAge: cdk.Duration.minutes(10),
      },
    });
    const accessLogs = new logs.LogGroup(this, 'ApiAccessLogs', {
      encryptionKey: logKey,
      retention: props.logRetention,
      removalPolicy: props.logRemovalPolicy,
    });
    new apigw.HttpStage(this, 'DefaultStage', {
      httpApi: this.api,
      stageName: '$default',
      autoDeploy: true,
      throttle: { rateLimit: 20, burstLimit: 40 },
      accessLogSettings: { destination: new apigw.LogGroupLogDestination(accessLogs) },
    });

    const authorizer = new HttpUserPoolAuthorizer('PlayerAuthorizer', this.userPool, {
      userPoolClients: [this.userPoolClient],
    });
    const integration = new HttpLambdaIntegration('Matchmaking', fn);
    this.api.addRoutes({ path: '/matchmaking', methods: [apigw.HttpMethod.POST], integration, authorizer });
    this.api.addRoutes({
      path: '/matchmaking/{ticketId}',
      methods: [apigw.HttpMethod.GET, apigw.HttpMethod.DELETE],
      integration,
      authorizer,
    });

    new cdk.CfnOutput(this, 'ApiUrl', { value: this.api.apiEndpoint });
    new cdk.CfnOutput(this, 'UserPoolId', { value: this.userPool.userPoolId });
    new cdk.CfnOutput(this, 'UserPoolClientId', { value: this.userPoolClient.userPoolClientId });
  }
}
