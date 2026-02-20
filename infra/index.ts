import * as pulumi from "@pulumi/pulumi";
import * as aws from "@pulumi/aws";
import { registerAutoTags } from "./autotags";

const coreInfra = new pulumi.StackReference(`honeycomb-devrel/observaquiz-coreinfra/${pulumi.getStack()}`);
const apigatewayId = coreInfra.requireOutput("apiGatewayId");
const collectorHostName = coreInfra.requireOutput("collectorHostname");
const gateway = apigatewayId.apply((id) => aws.apigatewayv2.getApi({ apiId: id }));
const config = new pulumi.Config();
const openAIKey = config.requireSecret("openai-api-key");
const queryDataApiKey = config.requireSecret("query-data-api-key");
const deepchecksApiKey = config.requireSecret("deepchecks-api-key");
registerAutoTags({
  "project": "observaquiz",
  "stack": pulumi.getStack(),
})

const lambdaLoggingPolicyDocument = aws.iam.getPolicyDocument({
  statements: [
    {
      effect: "Allow",
      actions: ["logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"],
      resources: ["arn:aws:logs:*:*:*"],
    },
  ],
});

const lambdaExecutionRole = new aws.iam.Role("execution-role", {
  assumeRolePolicy: aws.iam.assumeRolePolicyForPrincipal({ Service: "lambda.amazonaws.com" }),
  inlinePolicies: [
    {
      name: "executionPolicy",
      policy: lambdaLoggingPolicyDocument.then((policy) => policy.json),
    },
  ],
});

const apiLambda = new aws.lambda.Function("api-lambda", {
  role: lambdaExecutionRole.arn,
  runtime: "provided.al2023",

  code: new pulumi.asset.FileArchive("../deploy/api.zip"),
  handler: "bootstrap",
  timeout: 40,
  environment: {
    variables: {
      openai_key: openAIKey,
      OTEL_EXPORTER_OTLP_ENDPOINT: pulumi.interpolate`http://${collectorHostName}`,
      OTEL_EXPORTER_OTLP_INSECURE: "true",
      DEEPCHECKS_ENV_TYPE: "Production",
      query_data_api_key: queryDataApiKey,
      deepchecks_api_key: deepchecksApiKey,
    },
  },
});

const deepChecksLambda = new aws.lambda.Function("deepchecks-lambda", {
  role: lambdaExecutionRole.arn,
  runtime: "provided.al2023",

  code: new pulumi.asset.FileArchive("../deploy/deepchecks_callback.zip"),
  handler: "bootstrap",
  timeout: 40,
  environment: {
    variables: {
      OTEL_EXPORTER_OTLP_ENDPOINT: pulumi.interpolate`http://${collectorHostName}`,
      OTEL_EXPORTER_OTLP_INSECURE: "true",
      DEEPCHECKS_ENV_TYPE: "Production",
    },
  },
});

const integration = new aws.apigatewayv2.Integration("api-gateway-integration", {
  apiId: gateway.id,
  integrationType: "AWS_PROXY",
  integrationUri: apiLambda.arn,
  payloadFormatVersion: "2.0",
});

const deepchecks_integration = new aws.apigatewayv2.Integration("api-gateway-integration-deepchecks", {
  apiId: gateway.id,
  integrationType: "AWS_PROXY",
  integrationUri: deepChecksLambda.arn,
  payloadFormatVersion: "2.0",
});

// attach integration to route
const route = new aws.apigatewayv2.Route("api-gateway-route", {
  apiId: gateway.id,
  routeKey: "$default",
  target: pulumi.interpolate`integrations/${integration.id}`,
});

const deepchecks_route = new aws.apigatewayv2.Route("api-gateway-route-deepchecks", {
  apiId: gateway.id,
  routeKey: "ANY /api/deepchecks/{proxy+}",
  target: pulumi.interpolate`integrations/${deepchecks_integration.id}`,
});

// api gateway stage
const stage = new aws.apigatewayv2.Stage("api-gateway-stage", {
  apiId: gateway.id,
  name: "$default",
  autoDeploy: true,
});

var lambdaPermission = new aws.lambda.Permission("api-lambda-permission", {
  action: "lambda:InvokeFunction",
  function: apiLambda.name,
  principal: "apigateway.amazonaws.com",
  sourceArn: pulumi.interpolate`${gateway.executionArn}/*/*`,
});

var deepchecks_lambdaPermission = new aws.lambda.Permission("api-lambda-permission-deepchecks", {
  action: "lambda:InvokeFunction",
  function: deepChecksLambda.name,
  principal: "apigateway.amazonaws.com",
  sourceArn: pulumi.interpolate`${gateway.executionArn}/*/*`,
});
