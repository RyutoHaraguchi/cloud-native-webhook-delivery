package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// SQS の制限値
const (
	// maxDelay は DelaySeconds で指定できる上限
	maxDelay = 15 * time.Minute
	// maxWaitTime は WaitTimeSeconds で指定できる上限
	maxWaitTime = 20 * time.Second
	// maxReceiveMessages は 1 回の ReceiveMessage で受け取れる上限
	maxReceiveMessages = 10
)

// DeliveryQueueConfig は DeliveryQueue の設定。値は cmd/ が環境変数から読んで渡す
type DeliveryQueueConfig struct {
	// QueueURL は配信キューの URL
	QueueURL string
	// DLQURL は配信の DLQ の URL
	DLQURL string
	// WaitTime は Receive のロングポーリングの待ち時間。0〜20 秒の秒単位で指定する
	WaitTime time.Duration
}

// DeliveryQueue は SQS の配信キューに配信を入れ、受け取る
type DeliveryQueue struct {
	client      *sqs.Client
	queueURL    string
	dlqURL      string
	waitSeconds int32
}

var (
	_ core.DeliveryPublisher = (*DeliveryQueue)(nil)
	_ core.DeliveryConsumer  = (*DeliveryQueue)(nil)
)

// NewDeliveryQueue は DeliveryQueue を作る。URL が空、または WaitTime が 0〜20 秒の秒単位でなければエラーを返す
func NewDeliveryQueue(client *sqs.Client, cfg DeliveryQueueConfig) (*DeliveryQueue, error) {
	if cfg.QueueURL == "" {
		return nil, errors.New("delivery queue: queue url is empty")
	}
	if cfg.DLQURL == "" {
		return nil, errors.New("delivery queue: dlq url is empty")
	}
	if cfg.WaitTime < 0 || cfg.WaitTime > maxWaitTime || cfg.WaitTime%time.Second != 0 {
		return nil, fmt.Errorf("delivery queue: wait time %s must be whole seconds between 0 and %s", cfg.WaitTime, maxWaitTime)
	}
	return &DeliveryQueue{
		client:      client,
		queueURL:    cfg.QueueURL,
		dlqURL:      cfg.DLQURL,
		waitSeconds: int32(cfg.WaitTime / time.Second),
	}, nil
}

// Send は delay だけ遅らせて配信を本キューに入れる
func (q *DeliveryQueue) Send(ctx context.Context, key core.DeliveryKey, delay time.Duration) error {
	seconds, err := delaySeconds(delay)
	if err != nil {
		return fmt.Errorf("send delivery: %w", err)
	}
	if err := q.send(ctx, q.queueURL, key, seconds); err != nil {
		return fmt.Errorf("send delivery: %w", err)
	}
	return nil
}

// SendToDLQ は配信を DLQ に入れる
func (q *DeliveryQueue) SendToDLQ(ctx context.Context, key core.DeliveryKey) error {
	if err := q.send(ctx, q.dlqURL, key, 0); err != nil {
		return fmt.Errorf("send delivery to dlq: %w", err)
	}
	return nil
}

// send は鍵を検証し、本文にしてキューに入れる。MessageGroupId にはテナント ID を指定する
func (q *DeliveryQueue) send(ctx context.Context, queueURL string, key core.DeliveryKey, delaySeconds int32) error {
	if err := key.Validate(); err != nil {
		return err
	}
	body, err := encodeKey(key)
	if err != nil {
		return err
	}
	if _, err := q.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:       awssdk.String(queueURL),
		MessageBody:    awssdk.String(body),
		MessageGroupId: awssdk.String(key.TenantID),
		DelaySeconds:   delaySeconds,
	}); err != nil {
		return fmt.Errorf("sqs send message: %w", err)
	}
	return nil
}

// Receive は最大 max 件の配信を受け取る。max は 1〜10 で、範囲外なら SQS を呼ばずにエラーを返す。
// 本文を読めないメッセージは、その 1 件の Err に理由を入れて返し、バッチ全体はエラーにしない
func (q *DeliveryQueue) Receive(ctx context.Context, max int) ([]core.QueuedDelivery, error) {
	if max < 1 || max > maxReceiveMessages {
		return nil, fmt.Errorf("receive deliveries: max %d must be between 1 and %d", max, maxReceiveMessages)
	}
	out, err := q.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            awssdk.String(q.queueURL),
		MaxNumberOfMessages: int32(max),
		WaitTimeSeconds:     q.waitSeconds,
	})
	if err != nil {
		return nil, fmt.Errorf("receive deliveries: sqs receive message: %w", err)
	}
	deliveries := make([]core.QueuedDelivery, 0, len(out.Messages))
	for _, m := range out.Messages {
		d := core.QueuedDelivery{Receipt: awssdk.ToString(m.ReceiptHandle)}
		key, err := decodeKey(awssdk.ToString(m.Body))
		if err != nil {
			d.Err = fmt.Errorf("sqs message %s: %w", awssdk.ToString(m.MessageId), err)
		} else {
			d.Key = key
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, nil
}

// Ack は受け取った配信を本キューから消す
func (q *DeliveryQueue) Ack(ctx context.Context, receipt string) error {
	if _, err := q.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      awssdk.String(q.queueURL),
		ReceiptHandle: awssdk.String(receipt),
	}); err != nil {
		return fmt.Errorf("ack delivery: sqs delete message: %w", err)
	}
	return nil
}

// delaySeconds は遅延を DelaySeconds の秒数にする。負は 0、秒の端数は切り上げる。
// 早く届けると再送の間隔が縮むので、切り捨てない。15 分を超えれば ErrDelayTooLong を返す
func delaySeconds(d time.Duration) (int32, error) {
	if d > maxDelay {
		return 0, fmt.Errorf("%w: %s exceeds %s", core.ErrDelayTooLong, d, maxDelay)
	}
	if d <= 0 {
		return 0, nil
	}
	return int32((d + time.Second - 1) / time.Second), nil
}

// queueBody は SQS の本文。ペイロードは載せず、配信を指す 3 つの ID だけを載せる
type queueBody struct {
	TenantID   string `json:"tenantId"`
	MessageID  string `json:"messageId"`
	EndpointID string `json:"endpointId"`
}

// encodeKey は鍵を SQS の本文にする
func encodeKey(k core.DeliveryKey) (string, error) {
	b, err := json.Marshal(queueBody{TenantID: k.TenantID, MessageID: k.MessageID, EndpointID: k.EndpointID})
	if err != nil {
		return "", fmt.Errorf("encode delivery key: %w", err)
	}
	return string(b), nil
}

// decodeKey は SQS の本文を鍵にして検証する。知らないフィールドは無視し、将来フィールドを足しても古いワーカーが読めるようにする
func decodeKey(body string) (core.DeliveryKey, error) {
	var v queueBody
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return core.DeliveryKey{}, fmt.Errorf("decode delivery key: %w", err)
	}
	k := core.DeliveryKey{TenantID: v.TenantID, MessageID: v.MessageID, EndpointID: v.EndpointID}
	if err := k.Validate(); err != nil {
		return core.DeliveryKey{}, fmt.Errorf("decode delivery key: %w", err)
	}
	return k, nil
}
