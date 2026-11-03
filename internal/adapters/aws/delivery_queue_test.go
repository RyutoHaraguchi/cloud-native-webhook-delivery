//go:build integration

package aws

import (
	"context"
	"errors"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// テストのキューの可視性タイムアウトと、DLQ へ移すまでの受信回数
const (
	testVisibility      = time.Second
	testMaxReceiveCount = 2
)

// queueTestKey は MessageGroupId を確かめやすいよう、テナント ID を t_1 に固定した鍵を返す
func queueTestKey(messageID string) core.DeliveryKey {
	return core.DeliveryKey{TenantID: "t_1", MessageID: messageID, EndpointID: "ep_1"}
}

// newTestDeliveryQueue はテストごとのキューと DLQ を作り、それを使う DeliveryQueue を返す
func newTestDeliveryQueue(t *testing.T, waitTime time.Duration) (q *DeliveryQueue, client *sqs.Client, queueURL, dlqURL string) {
	t.Helper()
	client = testSQS(t)
	queueURL, dlqURL = testQueues(t, client, testVisibility, testMaxReceiveCount)
	q, err := NewDeliveryQueue(client, DeliveryQueueConfig{QueueURL: queueURL, DLQURL: dlqURL, WaitTime: waitTime})
	if err != nil {
		t.Fatalf("NewDeliveryQueue err = %v", err)
	}
	return q, client, queueURL, dlqURL
}

// receive は DeliveryQueue から最大 10 件受け取る。エラーなら失敗させる
func receive(t *testing.T, q *DeliveryQueue) []core.QueuedDelivery {
	t.Helper()
	got, err := q.Receive(t.Context(), 10)
	if err != nil {
		t.Fatalf("Receive err = %v", err)
	}
	return got
}

// rawReceive はアダプタを通さずに最大 10 件受け取る。MessageGroupId も受け取る
func rawReceive(t *testing.T, client *sqs.Client, queueURL string) []sqstypes.Message {
	t.Helper()
	out, err := client.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{
		QueueUrl:                    awssdk.String(queueURL),
		MaxNumberOfMessages:         10,
		MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameMessageGroupId},
	})
	if err != nil {
		t.Fatalf("ReceiveMessage err = %v", err)
	}
	return out.Messages
}

// rawSend はアダプタを通さずに本文をそのまま送る
func rawSend(t *testing.T, client *sqs.Client, queueURL, body string) {
	t.Helper()
	if _, err := client.SendMessage(t.Context(), &sqs.SendMessageInput{
		QueueUrl:    awssdk.String(queueURL),
		MessageBody: awssdk.String(body),
	}); err != nil {
		t.Fatalf("SendMessage err = %v", err)
	}
}

func TestDeliveryQueue_SendReceive(t *testing.T) {
	q, _, _, _ := newTestDeliveryQueue(t, time.Second)
	key := queueTestKey("m_1")

	if err := q.Send(t.Context(), key, 0); err != nil {
		t.Fatalf("Send err = %v", err)
	}

	var got []core.QueuedDelivery
	eventually(t, 10*time.Second, func() bool {
		got = receive(t, q)
		return len(got) > 0
	})
	if len(got) != 1 {
		t.Fatalf("受信 %d 件, want 1", len(got))
	}
	if got[0].Err != nil {
		t.Fatalf("Err = %v, want nil", got[0].Err)
	}
	if got[0].Key != key {
		t.Errorf("Key = %+v, want %+v", got[0].Key, key)
	}
	if got[0].Receipt == "" {
		t.Error("Receipt が空")
	}
}

func TestDeliveryQueue_MessageGroupId(t *testing.T) {
	q, client, queueURL, _ := newTestDeliveryQueue(t, time.Second)

	if err := q.Send(t.Context(), queueTestKey("m_1"), 0); err != nil {
		t.Fatalf("Send err = %v", err)
	}

	var msgs []sqstypes.Message
	eventually(t, 10*time.Second, func() bool {
		msgs = rawReceive(t, client, queueURL)
		return len(msgs) > 0
	})
	if got := msgs[0].Attributes[string(sqstypes.MessageSystemAttributeNameMessageGroupId)]; got != "t_1" {
		t.Errorf("MessageGroupId = %q, want t_1", got)
	}
}

func TestDeliveryQueue_Delay(t *testing.T) {
	q, _, _, _ := newTestDeliveryQueue(t, time.Second)
	key := queueTestKey("m_1")

	if err := q.Send(t.Context(), key, 3*time.Second); err != nil {
		t.Fatalf("Send err = %v", err)
	}
	if got := receive(t, q); len(got) != 0 {
		t.Fatalf("遅延中に %d 件届いた, want 0", len(got))
	}

	var got []core.QueuedDelivery
	eventually(t, 10*time.Second, func() bool {
		got = receive(t, q)
		return len(got) > 0
	})
	if len(got) != 1 || got[0].Key != key {
		t.Errorf("受信 = %+v, want %+v の 1 件", got, key)
	}
}

func TestDeliveryQueue_DelayTooLong(t *testing.T) {
	q, _, _, _ := newTestDeliveryQueue(t, time.Second)

	err := q.Send(t.Context(), queueTestKey("m_1"), 16*time.Minute)
	if !errors.Is(err, core.ErrDelayTooLong) {
		t.Fatalf("Send err = %v, want ErrDelayTooLong", err)
	}
	if got := receive(t, q); len(got) != 0 {
		t.Errorf("受信 %d 件, want 0", len(got))
	}
}

func TestDeliveryQueue_BrokenBody(t *testing.T) {
	q, client, queueURL, dlqURL := newTestDeliveryQueue(t, time.Second)
	key := queueTestKey("m_1")

	rawSend(t, client, queueURL, "not json")
	if err := q.Send(t.Context(), key, 0); err != nil {
		t.Fatalf("Send err = %v", err)
	}

	var broken, valid *core.QueuedDelivery
	eventually(t, 10*time.Second, func() bool {
		for _, d := range receive(t, q) {
			if d.Err != nil {
				broken = &d
			} else {
				valid = &d
			}
		}
		return broken != nil && valid != nil
	})
	if broken.Receipt == "" {
		t.Error("壊れた方の Receipt が空")
	}
	if valid.Key != key {
		t.Errorf("正常な方の Key = %+v, want %+v", valid.Key, key)
	}
	// 正常な方は DLQ に移らないよう消しておく
	if err := q.Ack(t.Context(), valid.Receipt); err != nil {
		t.Fatalf("Ack err = %v", err)
	}

	// 壊れた方を消さずに受信を続けると、受信回数の上限で DLQ に移る
	eventually(t, 20*time.Second, func() bool {
		receive(t, q)
		for _, m := range rawReceive(t, client, dlqURL) {
			if awssdk.ToString(m.Body) == "not json" {
				return true
			}
		}
		return false
	})
}

func TestDeliveryQueue_Ack(t *testing.T) {
	q, _, _, _ := newTestDeliveryQueue(t, time.Second)
	acked := queueTestKey("m_acked")
	kept := queueTestKey("m_kept")

	for _, k := range []core.DeliveryKey{acked, kept} {
		if err := q.Send(t.Context(), k, 0); err != nil {
			t.Fatalf("Send err = %v", err)
		}
	}

	receipts := map[core.DeliveryKey]string{}
	eventually(t, 10*time.Second, func() bool {
		for _, d := range receive(t, q) {
			receipts[d.Key] = d.Receipt
		}
		return receipts[acked] != "" && receipts[kept] != ""
	})
	if err := q.Ack(t.Context(), receipts[acked]); err != nil {
		t.Fatalf("Ack err = %v", err)
	}

	// 可視性タイムアウトの 2 倍待ってから受け直す。Ack したものは届かず、Ack しないものは再び届く
	time.Sleep(2 * testVisibility)
	eventually(t, 10*time.Second, func() bool {
		seen := false
		for _, d := range receive(t, q) {
			if d.Key == acked {
				t.Fatalf("Ack した %+v が再び届いた", acked)
			}
			if d.Key == kept {
				seen = true
			}
		}
		return seen
	})
}

func TestDeliveryQueue_SendToDLQ(t *testing.T) {
	q, client, _, dlqURL := newTestDeliveryQueue(t, time.Second)
	key := queueTestKey("m_1")

	if err := q.SendToDLQ(t.Context(), key); err != nil {
		t.Fatalf("SendToDLQ err = %v", err)
	}

	var msgs []sqstypes.Message
	eventually(t, 10*time.Second, func() bool {
		msgs = rawReceive(t, client, dlqURL)
		return len(msgs) > 0
	})
	want := `{"tenantId":"t_1","messageId":"m_1","endpointId":"ep_1"}`
	if got := awssdk.ToString(msgs[0].Body); got != want {
		t.Errorf("本文 = %s, want %s", got, want)
	}
	if got := msgs[0].Attributes[string(sqstypes.MessageSystemAttributeNameMessageGroupId)]; got != "t_1" {
		t.Errorf("MessageGroupId = %q, want t_1", got)
	}
}

func TestDeliveryQueue_InvalidID(t *testing.T) {
	q, client, _, dlqURL := newTestDeliveryQueue(t, time.Second)
	key := core.DeliveryKey{TenantID: "t#1", MessageID: "m_1", EndpointID: "ep_1"}

	if err := q.Send(t.Context(), key, 0); !errors.Is(err, core.ErrInvalidKey) {
		t.Errorf("Send err = %v, want ErrInvalidKey", err)
	}
	if err := q.SendToDLQ(t.Context(), key); !errors.Is(err, core.ErrInvalidKey) {
		t.Errorf("SendToDLQ err = %v, want ErrInvalidKey", err)
	}
	if got := receive(t, q); len(got) != 0 {
		t.Errorf("本キューに %d 件, want 0", len(got))
	}
	if got := rawReceive(t, client, dlqURL); len(got) != 0 {
		t.Errorf("DLQ に %d 件, want 0", len(got))
	}
}

func TestDeliveryQueue_ReceiveCanceled(t *testing.T) {
	q, _, _, _ := newTestDeliveryQueue(t, 20*time.Second)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := q.Receive(ctx, 10)
		done <- err
	}()

	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Receive err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取り消してから 5 秒以内に Receive が戻らない")
	}
}

func TestDeliveryQueue_ReceiveMax(t *testing.T) {
	// client が nil なので、SQS を呼べば panic する。呼ばずにエラーを返すことを確かめる
	q, err := NewDeliveryQueue(nil, DeliveryQueueConfig{QueueURL: "http://q", DLQURL: "http://dlq", WaitTime: time.Second})
	if err != nil {
		t.Fatalf("NewDeliveryQueue err = %v", err)
	}
	for _, max := range []int{0, 11} {
		if _, err := q.Receive(t.Context(), max); err == nil {
			t.Errorf("Receive(max=%d) err = nil, want error", max)
		}
	}
}
