//go:build integration

package aws

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// 統合テストの接続先と認証情報。実環境に書き込まないよう、環境変数は読まずにローカルへ固定する
const (
	testDynamoDBEndpoint = "http://localhost:8000"
	testSQSEndpoint      = "http://localhost:9324"
	testRegion           = "ap-northeast-1"
	// 接続を確かめる呼び出しの期限
	testPingTimeout = 2 * time.Second
	// compose の dynamodb-init がテーブルを作り終えるまで待つ上限
	testTableWait = 30 * time.Second
)

// compose の dynamodb-init が作るテーブル
var testTables = []string{"messages", "config"}

func testCredentials() awssdk.CredentialsProvider {
	return credentials.NewStaticCredentialsProvider("local", "local", "")
}

// testDynamoDB は DynamoDB Local のクライアントを返す。
// 接続できない場合と、テーブルが ACTIVE にならない場合は、別の文言で失敗させる
func testDynamoDB(t *testing.T) *dynamodb.Client {
	t.Helper()
	client := dynamodb.New(dynamodb.Options{
		BaseEndpoint: awssdk.String(testDynamoDBEndpoint),
		Region:       testRegion,
		Credentials:  testCredentials(),
	})

	ctx, cancel := context.WithTimeout(t.Context(), testPingTimeout)
	defer cancel()
	if _, err := client.ListTables(ctx, &dynamodb.ListTablesInput{}); err != nil {
		t.Fatalf("docker compose up -d を実行する: DynamoDB Local (%s) に接続できない: %v", testDynamoDBEndpoint, err)
	}

	// 待ち全体に期限を付ける。DescribeTable が応答しなくなっても testTableWait で打ち切る
	waitCtx, waitCancel := context.WithTimeout(t.Context(), testTableWait)
	defer waitCancel()
	for _, table := range testTables {
		for {
			status, err := describeTableStatus(waitCtx, client, table)
			if err == nil && status == types.TableStatusActive {
				break
			}
			select {
			case <-waitCtx.Done():
				t.Fatalf("テーブル %s が %s 以内に ACTIVE にならない。docker compose logs dynamodb-init を確認する: status=%q, err=%v",
					table, testTableWait, status, err)
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	return client
}

func describeTableStatus(ctx context.Context, client *dynamodb.Client, table string) (types.TableStatus, error) {
	out, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: awssdk.String(table)})
	if err != nil {
		return "", err
	}
	return out.Table.TableStatus, nil
}

// testSQS は ElasticMQ のクライアントを返す。接続できなければ失敗させる
func testSQS(t *testing.T) *sqs.Client {
	t.Helper()
	client := sqs.New(sqs.Options{
		BaseEndpoint: awssdk.String(testSQSEndpoint),
		Region:       testRegion,
		Credentials:  testCredentials(),
	})

	ctx, cancel := context.WithTimeout(t.Context(), testPingTimeout)
	defer cancel()
	if _, err := client.ListQueues(ctx, &sqs.ListQueuesInput{}); err != nil {
		t.Fatalf("docker compose up -d を実行する: ElasticMQ (%s) に接続できない: %v", testSQSEndpoint, err)
	}
	return client
}

// randomHex は 16 文字のランダムな 16 進文字列を返す
func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("乱数を作れない: %v", err)
	}
	return hex.EncodeToString(b)
}

// testTenantID はテストごとに別のテナント ID を返す。共有のテーブルでテスト同士のデータを分ける
func testTenantID(t *testing.T) string {
	t.Helper()
	return "t_test_" + randomHex(t)
}

// testQueues は本キューと DLQ をテストごとに作り、終了時に両方を消す。
// 本キューには可視性タイムアウトと、maxReceiveCount 回で DLQ へ移す設定を付ける
func testQueues(t *testing.T, client *sqs.Client, visibility time.Duration, maxReceiveCount int) (queueURL, dlqURL string) {
	t.Helper()
	ctx := t.Context()
	name := "test-" + randomHex(t)

	dlq, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: awssdk.String(name + "-dlq")})
	if err != nil {
		t.Fatalf("DLQ を作れない: %v", err)
	}
	dlqURL = awssdk.ToString(dlq.QueueUrl)
	t.Cleanup(func() { deleteTestQueue(t, client, dlqURL) })

	attrs, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       awssdk.String(dlqURL),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatalf("DLQ の ARN を取れない: %v", err)
	}
	dlqARN := attrs.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]
	if dlqARN == "" {
		t.Fatal("DLQ の ARN が空")
	}

	redrive, err := json.Marshal(map[string]any{
		"deadLetterTargetArn": dlqARN,
		"maxReceiveCount":     maxReceiveCount,
	})
	if err != nil {
		t.Fatalf("RedrivePolicy を作れない: %v", err)
	}
	queue, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: awssdk.String(name),
		Attributes: map[string]string{
			string(sqstypes.QueueAttributeNameVisibilityTimeout): strconv.Itoa(int(visibility / time.Second)),
			string(sqstypes.QueueAttributeNameRedrivePolicy):     string(redrive),
		},
	})
	if err != nil {
		t.Fatalf("キューを作れない: %v", err)
	}
	queueURL = awssdk.ToString(queue.QueueUrl)
	t.Cleanup(func() { deleteTestQueue(t, client, queueURL) })

	return queueURL, dlqURL
}

// deleteTestQueue は t.Cleanup から呼ぶ。t.Context() は取り消し済みなので context.Background() を使う。
// 消せなくても後のテストは別名のキューを使うので、失敗にせずログに残す
func deleteTestQueue(t *testing.T, client *sqs.Client, queueURL string) {
	t.Helper()
	if _, err := client.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: awssdk.String(queueURL)}); err != nil {
		t.Logf("キュー %s を消せない: %v", queueURL, err)
	}
}

// eventually は cond が真になるまで 200ms ごとに呼ぶ。timeout までに真にならなければ失敗させる
func eventually(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s 以内に条件が満たされない", timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// putRawItem はアダプタを通さずに項目を書き込む。壊れた保存値を用意するのに使う
func putRawItem(t *testing.T, client *dynamodb.Client, table string, item map[string]types.AttributeValue) {
	t.Helper()
	if _, err := client.PutItem(t.Context(), &dynamodb.PutItemInput{
		TableName: awssdk.String(table),
		Item:      item,
	}); err != nil {
		t.Fatalf("%s に項目を書き込めない: %v", table, err)
	}
}

// getRawItem はアダプタを通さずに項目を読む。なければ nil を返す
func getRawItem(t *testing.T, client *dynamodb.Client, table, pk, sk string) map[string]types.AttributeValue {
	t.Helper()
	out, err := client.GetItem(t.Context(), &dynamodb.GetItemInput{
		TableName: awssdk.String(table),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: pk},
			"SK": &types.AttributeValueMemberS{Value: sk},
		},
		ConsistentRead: awssdk.Bool(true),
	})
	if err != nil {
		t.Fatalf("%s から項目を読めない (PK=%s, SK=%s): %v", table, pk, sk, err)
	}
	if len(out.Item) == 0 {
		return nil
	}
	return out.Item
}
