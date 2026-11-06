//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/adapters/aws"
	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// 通しのテストの接続先と認証情報。実環境に書き込まないよう、環境変数は読まずにローカルへ固定する
const (
	e2eDynamoDBEndpoint = "http://localhost:8000"
	e2eSQSEndpoint      = "http://localhost:9324"
	e2eRegion           = "ap-northeast-1"
	// compose の deliveries キュー。ホストからは localhost で同じキューに届く
	e2eQueueURL = "http://localhost:9324/000000000000/deliveries"
	e2eDLQURL   = "http://localhost:9324/000000000000/deliveries-dlq"
	// compose の seed が書くテナント
	e2eTenantID = "t_local"
	// 接続を確かめる呼び出しの期限
	e2ePingTimeout = 2 * time.Second
	// delivered になるまで待つ上限と、読む間隔
	e2eDeliveredWait = 30 * time.Second
	e2ePollInterval  = 500 * time.Millisecond
)

const e2eStartHint = "docker compose up -d --build を実行する"

func e2eCredentials() awssdk.CredentialsProvider {
	return credentials.NewStaticCredentialsProvider("local", "local", "")
}

// e2eDynamoDB は DynamoDB Local のクライアントを返す。接続できなければ失敗させる
func e2eDynamoDB(t *testing.T) *dynamodb.Client {
	t.Helper()
	client := dynamodb.New(dynamodb.Options{
		BaseEndpoint: awssdk.String(e2eDynamoDBEndpoint),
		Region:       e2eRegion,
		Credentials:  e2eCredentials(),
	})
	ctx, cancel := context.WithTimeout(t.Context(), e2ePingTimeout)
	defer cancel()
	if _, err := client.ListTables(ctx, &dynamodb.ListTablesInput{}); err != nil {
		t.Fatalf("%s: DynamoDB Local (%s) に接続できない: %v", e2eStartHint, e2eDynamoDBEndpoint, err)
	}
	return client
}

// e2eSQS は ElasticMQ のクライアントを返す。接続できないか、compose のキューがなければ失敗させる
func e2eSQS(t *testing.T) *sqs.Client {
	t.Helper()
	client := sqs.New(sqs.Options{
		BaseEndpoint: awssdk.String(e2eSQSEndpoint),
		Region:       e2eRegion,
		Credentials:  e2eCredentials(),
	})
	ctx, cancel := context.WithTimeout(t.Context(), e2ePingTimeout)
	defer cancel()
	if _, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: awssdk.String(e2eQueueURL)}); err != nil {
		t.Fatalf("%s: ElasticMQ (%s) のキュー %s を確かめられない: %v", e2eStartHint, e2eSQSEndpoint, e2eQueueURL, err)
	}
	return client
}

// e2eMessageID はランダムなメッセージ ID を返す。共有のテーブルでテスト同士のデータを分ける
func e2eMessageID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("乱数を作れない: %v", err)
	}
	return "msg_" + hex.EncodeToString(b)
}

// TestDeliveryWorkerEndToEnd は compose で動く delivery-worker が、
// キューに入れた配信を dummy-receiver へ送り、delivered を記録することを確かめる。
// dummy-receiver は署名が合わなければ 401 を返すので、delivered は署名の検証を通った証明になる
func TestDeliveryWorkerEndToEnd(t *testing.T) {
	dynamo := e2eDynamoDB(t)
	sqsClient := e2eSQS(t)

	repo, err := aws.NewMessageRepository(dynamo, aws.MessageRepositoryConfig{
		Table:     "messages",
		Retention: time.Hour,
	}, time.Now)
	if err != nil {
		t.Fatalf("NewMessageRepository() error = %v", err)
	}
	queue, err := aws.NewDeliveryQueue(sqsClient, aws.DeliveryQueueConfig{
		QueueURL: e2eQueueURL,
		DLQURL:   e2eDLQURL,
	})
	if err != nil {
		t.Fatalf("NewDeliveryQueue() error = %v", err)
	}

	tests := []struct {
		endpointID   string
		eventType    string
		wantAttempts int
	}{
		// 1 回目で 200 を返す
		{endpointID: "ep_ok", eventType: "test.ok", wantAttempts: 1},
		// 2 回 503 を返し、3 回目で 200 を返す
		{endpointID: "ep_flaky", eventType: "test.retry", wantAttempts: 3},
	}
	for _, tt := range tests {
		t.Run(tt.endpointID, func(t *testing.T) {
			ctx := t.Context()
			msg := core.Message{
				TenantID:  e2eTenantID,
				ID:        e2eMessageID(t),
				Type:      tt.eventType,
				Payload:   []byte(`{"hello":"world"}`),
				CreatedAt: time.Now(),
			}
			key := core.DeliveryKey{TenantID: msg.TenantID, MessageID: msg.ID, EndpointID: tt.endpointID}
			if err := repo.CreateMessage(ctx, msg); err != nil {
				t.Fatalf("CreateMessage() error = %v", err)
			}
			if err := repo.CreateDelivery(ctx, key); err != nil {
				t.Fatalf("CreateDelivery() error = %v", err)
			}
			if err := queue.Send(ctx, key, 0); err != nil {
				t.Fatalf("Send() error = %v", err)
			}

			deadline := time.Now().Add(e2eDeliveredWait)
			var last core.Delivery
			for {
				d, err := repo.GetDelivery(ctx, key)
				if err != nil {
					t.Fatalf("GetDelivery() error = %v", err)
				}
				last = d
				if d.Status() == core.DeliveryStatusDelivered {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s 以内に delivered にならない (status=%s, attemptCount=%d, messageId=%s)。docker compose logs delivery-worker を確認する",
						e2eDeliveredWait, d.Status(), d.AttemptCount(), msg.ID)
				}
				select {
				case <-ctx.Done():
					t.Fatalf("待っている間に取り消された: %v", ctx.Err())
				case <-time.After(e2ePollInterval):
				}
			}
			if got := last.AttemptCount(); got != tt.wantAttempts {
				t.Errorf("AttemptCount() = %d, want %d", got, tt.wantAttempts)
			}
		})
	}
}
