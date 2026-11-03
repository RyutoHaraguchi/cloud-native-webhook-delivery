module github.com/RyutoHaraguchi/cloud-native-webhook-delivery

go 1.27.3

tool golang.org/x/vuln/cmd/govulncheck

require (
	github.com/aws/aws-sdk-go-v2 v1.47.4
	github.com/aws/aws-sdk-go-v2/credentials v1.20.10
	github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue v1.21.15
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.72.1
	github.com/aws/aws-sdk-go-v2/service/sqs v1.52.6
	github.com/standard-webhooks/standard-webhooks/libraries v0.0.1
)

require (
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/dynamodbstreams v1.46.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.22 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.13.7 // indirect
	github.com/aws/smithy-go v1.28.6 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/telemetry v0.0.0-20261010193011-f71dc92da45e // indirect
	golang.org/x/tools v0.52.0 // indirect
	golang.org/x/vuln v1.9.0 // indirect
)
