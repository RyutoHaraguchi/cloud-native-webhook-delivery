//go:build integration

package aws

import (
	"bytes"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// testMessagesTable は compose が作る messages テーブルの名前
const testMessagesTable = "messages"

// testRetention はテストで使う保持期間
const testRetention = 30 * 24 * time.Hour

// testClock はテストから進められる時計。JST の固定時刻から始める
type testClock struct {
	t time.Time
}

func newTestClock() *testClock {
	jst := time.FixedZone("JST", 9*60*60)
	return &testClock{t: time.Date(2026, 11, 3, 10, 0, 0, 123456789, jst)}
}

func (c *testClock) now() time.Time { return c.t }

func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestMessageRepository(t *testing.T) (*MessageRepository, *dynamodb.Client, *testClock) {
	t.Helper()
	client := testDynamoDB(t)
	clock := newTestClock()
	repo, err := NewMessageRepository(client, MessageRepositoryConfig{Table: testMessagesTable, Retention: testRetention}, clock.now)
	if err != nil {
		t.Fatalf("NewMessageRepository: %v", err)
	}
	return repo, client, clock
}

func testMessage(tenantID, messageID string, clock *testClock) core.Message {
	return core.Message{
		TenantID:  tenantID,
		ID:        messageID,
		Type:      "order.created",
		Payload:   []byte(`{"orderId":"o_1"}`),
		CreatedAt: clock.now(),
	}
}

// rawNumber は項目の数値の属性を int64 で返す
func rawNumber(t *testing.T, item map[string]types.AttributeValue, name string) int64 {
	t.Helper()
	v, ok := item[name].(*types.AttributeValueMemberN)
	if !ok {
		t.Fatalf("attribute %q is not N: %#v", name, item[name])
	}
	n, err := strconv.ParseInt(v.Value, 10, 64)
	if err != nil {
		t.Fatalf("attribute %q: %v", name, err)
	}
	return n
}

// rawString は項目の文字列の属性を返す
func rawString(t *testing.T, item map[string]types.AttributeValue, name string) string {
	t.Helper()
	v, ok := item[name].(*types.AttributeValueMemberS)
	if !ok {
		t.Fatalf("attribute %q is not S: %#v", name, item[name])
	}
	return v.Value
}

func TestNewMessageRepository_InvalidConfig(t *testing.T) {
	client := &dynamodb.Client{}
	now := func() time.Time { return time.Time{} }
	tests := []struct {
		name string
		cfg  MessageRepositoryConfig
		now  func() time.Time
	}{
		{"empty table", MessageRepositoryConfig{Retention: time.Hour}, now},
		{"zero retention", MessageRepositoryConfig{Table: "messages"}, now},
		{"negative retention", MessageRepositoryConfig{Table: "messages", Retention: -time.Hour}, now},
		{"nil now", MessageRepositoryConfig{Table: "messages", Retention: time.Hour}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewMessageRepository(client, tt.cfg, tt.now); err == nil {
				t.Fatal("err = nil, want error")
			}
		})
	}
}

func TestMessageRepository_CreateAndGetMessage(t *testing.T) {
	repo, client, clock := newTestMessageRepository(t)
	tenantID := testTenantID(t)
	want := testMessage(tenantID, "msg_1", clock)

	if err := repo.CreateMessage(t.Context(), want); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	got, err := repo.GetMessage(t.Context(), tenantID, "msg_1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.TenantID != want.TenantID || got.ID != want.ID || got.Type != want.Type {
		t.Errorf("TenantID, ID, Type = %q, %q, %q, want %q, %q, %q", got.TenantID, got.ID, got.Type, want.TenantID, want.ID, want.Type)
	}
	if !bytes.Equal(got.Payload, want.Payload) {
		t.Errorf("Payload = %q, want %q", got.Payload, want.Payload)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}

	raw := getRawItem(t, client, testMessagesTable, messagePK(tenantID, "msg_1"), messageSK)
	if raw == nil {
		t.Fatal("raw item is nil")
	}
	if got := rawString(t, raw, "itemType"); got != "message" {
		t.Errorf("itemType = %q, want message", got)
	}
	if got, want := rawNumber(t, raw, "expiresAt"), clock.now().Add(testRetention).Unix(); got != want {
		t.Errorf("expiresAt = %d, want %d", got, want)
	}
}

func TestMessageRepository_CreateMessageDuplicate(t *testing.T) {
	repo, _, clock := newTestMessageRepository(t)
	tenantID := testTenantID(t)
	first := testMessage(tenantID, "msg_1", clock)
	if err := repo.CreateMessage(t.Context(), first); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	second := first
	second.Type = "order.updated"
	second.Payload = []byte(`{"orderId":"o_2"}`)
	if err := repo.CreateMessage(t.Context(), second); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}

	got, err := repo.GetMessage(t.Context(), tenantID, "msg_1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.Type != first.Type || !bytes.Equal(got.Payload, first.Payload) {
		t.Errorf("Type, Payload = %q, %q, want %q, %q", got.Type, got.Payload, first.Type, first.Payload)
	}
}

func TestMessageRepository_CreateMessageInvalid(t *testing.T) {
	repo, client, clock := newTestMessageRepository(t)
	tenantID := testTenantID(t)
	m := testMessage(tenantID, "msg_1", clock)
	m.Type = ""

	if err := repo.CreateMessage(t.Context(), m); !errors.Is(err, core.ErrInvalidMessage) {
		t.Fatalf("err = %v, want ErrInvalidMessage", err)
	}
	if raw := getRawItem(t, client, testMessagesTable, messagePK(tenantID, "msg_1"), messageSK); raw != nil {
		t.Errorf("raw item = %v, want nil", raw)
	}
}

func TestMessageRepository_GetMessageNotFound(t *testing.T) {
	repo, _, clock := newTestMessageRepository(t)
	if err := repo.CreateMessage(t.Context(), testMessage(testTenantID(t), "msg_1", clock)); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	_, err := repo.GetMessage(t.Context(), testTenantID(t), "msg_1")
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestMessageRepository_PayloadRoundTrip(t *testing.T) {
	repo, _, clock := newTestMessageRepository(t)
	tenantID := testTenantID(t)
	tests := []struct {
		name    string
		payload []byte
	}{
		{"binary", []byte{0xff, 0xfe, 0x00}},
		{"max size", bytes.Repeat([]byte("a"), core.MaxPayloadSize)},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testMessage(tenantID, "msg_"+strconv.Itoa(i), clock)
			m.Payload = tt.payload
			if err := repo.CreateMessage(t.Context(), m); err != nil {
				t.Fatalf("CreateMessage: %v", err)
			}
			got, err := repo.GetMessage(t.Context(), tenantID, m.ID)
			if err != nil {
				t.Fatalf("GetMessage: %v", err)
			}
			if !bytes.Equal(got.Payload, tt.payload) {
				t.Errorf("Payload length %d differs from %d", len(got.Payload), len(tt.payload))
			}
		})
	}
}

func testDeliveryKey(tenantID string) core.DeliveryKey {
	return core.DeliveryKey{TenantID: tenantID, MessageID: "msg_1", EndpointID: "ep_1"}
}

func TestMessageRepository_CreateDelivery(t *testing.T) {
	repo, client, _ := newTestMessageRepository(t)
	key := testDeliveryKey(testTenantID(t))

	if err := repo.CreateDelivery(t.Context(), key); err != nil {
		t.Fatalf("CreateDelivery: %v", err)
	}
	got, err := repo.GetDelivery(t.Context(), key)
	if err != nil {
		t.Fatalf("GetDelivery: %v", err)
	}
	if got.Status() != core.DeliveryStatusPending || got.AttemptCount() != 0 {
		t.Errorf("Status, AttemptCount = %q, %d, want pending, 0", got.Status(), got.AttemptCount())
	}

	raw := getRawItem(t, client, testMessagesTable, messagePK(key.TenantID, key.MessageID), deliverySK(key.EndpointID))
	if raw == nil {
		t.Fatal("raw item is nil")
	}
	if got := rawString(t, raw, "itemType"); got != "delivery" {
		t.Errorf("itemType = %q, want delivery", got)
	}
}

// retryingDelivery は pending から 503 の失敗を記録した retrying の配信を返す
func retryingDelivery(t *testing.T, now time.Time) core.Delivery {
	t.Helper()
	d, err := core.NewDelivery().RecordAttempt(core.AttemptResult{
		Outcome:    core.AttemptOutcomeRetryableFailure,
		StatusCode: 503,
	}, core.DefaultRetrySchedule, now, 0.5)
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if d.Status() != core.DeliveryStatusRetrying {
		t.Fatalf("Status = %q, want retrying", d.Status())
	}
	return d
}

func TestMessageRepository_CreateDeliveryIdempotent(t *testing.T) {
	repo, _, clock := newTestMessageRepository(t)
	key := testDeliveryKey(testTenantID(t))
	if err := repo.CreateDelivery(t.Context(), key); err != nil {
		t.Fatalf("CreateDelivery: %v", err)
	}
	if err := repo.SaveDelivery(t.Context(), key, core.NewDelivery(), retryingDelivery(t, clock.now())); err != nil {
		t.Fatalf("SaveDelivery: %v", err)
	}

	if err := repo.CreateDelivery(t.Context(), key); err != nil {
		t.Fatalf("second CreateDelivery: %v", err)
	}
	got, err := repo.GetDelivery(t.Context(), key)
	if err != nil {
		t.Fatalf("GetDelivery: %v", err)
	}
	if got.Status() != core.DeliveryStatusRetrying {
		t.Errorf("Status = %q, want retrying", got.Status())
	}
}

func TestMessageRepository_SaveDelivery(t *testing.T) {
	repo, client, clock := newTestMessageRepository(t)
	key := testDeliveryKey(testTenantID(t))
	if err := repo.CreateDelivery(t.Context(), key); err != nil {
		t.Fatalf("CreateDelivery: %v", err)
	}

	pending := core.NewDelivery()
	retrying := retryingDelivery(t, clock.now())
	if err := repo.SaveDelivery(t.Context(), key, pending, retrying); err != nil {
		t.Fatalf("SaveDelivery retrying: %v", err)
	}
	got, err := repo.GetDelivery(t.Context(), key)
	if err != nil {
		t.Fatalf("GetDelivery: %v", err)
	}
	if got.Status() != core.DeliveryStatusRetrying || got.AttemptCount() != 1 || got.AttemptSeq() != 1 {
		t.Errorf("Status, AttemptCount, AttemptSeq = %q, %d, %d, want retrying, 1, 1", got.Status(), got.AttemptCount(), got.AttemptSeq())
	}
	if !got.NextAttemptAt().Equal(retrying.NextAttemptAt()) {
		t.Errorf("NextAttemptAt = %v, want %v", got.NextAttemptAt(), retrying.NextAttemptAt())
	}

	canceled, err := retrying.Cancel()
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := repo.SaveDelivery(t.Context(), key, retrying, canceled); err != nil {
		t.Fatalf("SaveDelivery canceled: %v", err)
	}
	got, err = repo.GetDelivery(t.Context(), key)
	if err != nil {
		t.Fatalf("GetDelivery: %v", err)
	}
	if got.Status() != core.DeliveryStatusCanceled {
		t.Errorf("Status = %q, want canceled", got.Status())
	}
	raw := getRawItem(t, client, testMessagesTable, messagePK(key.TenantID, key.MessageID), deliverySK(key.EndpointID))
	if _, ok := raw["nextAttemptAt"]; ok {
		t.Errorf("nextAttemptAt = %v, want removed", raw["nextAttemptAt"])
	}
}

func TestMessageRepository_SaveDeliveryConflict(t *testing.T) {
	repo, _, clock := newTestMessageRepository(t)
	key := testDeliveryKey(testTenantID(t))
	if err := repo.CreateDelivery(t.Context(), key); err != nil {
		t.Fatalf("CreateDelivery: %v", err)
	}

	pending := core.NewDelivery()
	first := retryingDelivery(t, clock.now())
	if err := repo.SaveDelivery(t.Context(), key, pending, first); err != nil {
		t.Fatalf("first SaveDelivery: %v", err)
	}
	second, err := pending.Cancel()
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := repo.SaveDelivery(t.Context(), key, pending, second); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("second SaveDelivery err = %v, want ErrConflict", err)
	}
	got, err := repo.GetDelivery(t.Context(), key)
	if err != nil {
		t.Fatalf("GetDelivery: %v", err)
	}
	if got.Status() != core.DeliveryStatusRetrying || got.AttemptCount() != 1 {
		t.Errorf("Status, AttemptCount = %q, %d, want retrying, 1", got.Status(), got.AttemptCount())
	}

	missing := core.DeliveryKey{TenantID: key.TenantID, MessageID: "msg_missing", EndpointID: "ep_1"}
	if err := repo.SaveDelivery(t.Context(), missing, pending, first); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("SaveDelivery to missing err = %v, want ErrConflict", err)
	}
}

func TestMessageRepository_SaveDeliveryKeepsExpiresAt(t *testing.T) {
	repo, client, clock := newTestMessageRepository(t)
	key := testDeliveryKey(testTenantID(t))
	if err := repo.CreateDelivery(t.Context(), key); err != nil {
		t.Fatalf("CreateDelivery: %v", err)
	}
	want := clock.now().Add(testRetention).Unix()

	clock.advance(time.Hour)
	if err := repo.SaveDelivery(t.Context(), key, core.NewDelivery(), retryingDelivery(t, clock.now())); err != nil {
		t.Fatalf("SaveDelivery: %v", err)
	}
	raw := getRawItem(t, client, testMessagesTable, messagePK(key.TenantID, key.MessageID), deliverySK(key.EndpointID))
	if got := rawNumber(t, raw, "expiresAt"); got != want {
		t.Errorf("expiresAt = %d, want %d", got, want)
	}
}

// deliveryRawItem は配信の保存値を作る。テストごとに属性を消したり差し替えたりして使う
func deliveryRawItem(key core.DeliveryKey, status, attemptCount string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK":           &types.AttributeValueMemberS{Value: messagePK(key.TenantID, key.MessageID)},
		"SK":           &types.AttributeValueMemberS{Value: deliverySK(key.EndpointID)},
		"itemType":     &types.AttributeValueMemberS{Value: "delivery"},
		"status":       &types.AttributeValueMemberS{Value: status},
		"attemptCount": &types.AttributeValueMemberN{Value: attemptCount},
		"attemptSeq":   &types.AttributeValueMemberN{Value: attemptCount},
	}
}

func TestMessageRepository_GetDeliveryMissingAttribute(t *testing.T) {
	repo, client, _ := newTestMessageRepository(t)
	tests := []struct {
		name  string
		edit  func(item map[string]types.AttributeValue)
		names []string
	}{
		{
			name:  "missing attemptSeq",
			edit:  func(item map[string]types.AttributeValue) { delete(item, "attemptSeq") },
			names: []string{"attemptSeq"},
		},
		{
			name: "NULL attemptCount and attemptSeq",
			edit: func(item map[string]types.AttributeValue) {
				item["attemptCount"] = &types.AttributeValueMemberNULL{Value: true}
				item["attemptSeq"] = &types.AttributeValueMemberNULL{Value: true}
			},
			names: []string{"attemptCount", "attemptSeq"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := testDeliveryKey(testTenantID(t))
			item := deliveryRawItem(key, "pending", "0")
			tt.edit(item)
			putRawItem(t, client, testMessagesTable, item)

			_, err := repo.GetDelivery(t.Context(), key)
			if err == nil {
				t.Fatal("err = nil, want error")
			}
			for _, name := range tt.names {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("err = %v, want it to name %s", err, name)
				}
			}
		})
	}
}

func TestMessageRepository_GetDeliveryInvalid(t *testing.T) {
	repo, client, _ := newTestMessageRepository(t)
	key := testDeliveryKey(testTenantID(t))
	putRawItem(t, client, testMessagesTable, deliveryRawItem(key, "retrying", "0"))

	_, err := repo.GetDelivery(t.Context(), key)
	if !errors.Is(err, core.ErrInvalidDelivery) {
		t.Fatalf("err = %v, want ErrInvalidDelivery", err)
	}
}

func TestMessageRepository_InvalidID(t *testing.T) {
	repo, _, _ := newTestMessageRepository(t)
	key := core.DeliveryKey{TenantID: "t#x", MessageID: "msg_1", EndpointID: "ep_1"}

	if _, err := repo.GetMessage(t.Context(), "t#x", "msg_1"); !errors.Is(err, core.ErrInvalidKey) {
		t.Errorf("GetMessage err = %v, want ErrInvalidKey", err)
	}
	if _, err := repo.GetDelivery(t.Context(), key); !errors.Is(err, core.ErrInvalidKey) {
		t.Errorf("GetDelivery err = %v, want ErrInvalidKey", err)
	}
	if err := repo.CreateDelivery(t.Context(), key); !errors.Is(err, core.ErrInvalidKey) {
		t.Errorf("CreateDelivery err = %v, want ErrInvalidKey", err)
	}
	pending := core.NewDelivery()
	canceled, err := pending.Cancel()
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := repo.SaveDelivery(t.Context(), key, pending, canceled); !errors.Is(err, core.ErrInvalidKey) {
		t.Errorf("SaveDelivery err = %v, want ErrInvalidKey", err)
	}
	if err := repo.SaveAttempt(t.Context(), key, pending, canceled, core.Attempt{}); !errors.Is(err, core.ErrInvalidKey) {
		t.Errorf("SaveAttempt err = %v, want ErrInvalidKey", err)
	}
}

// recordAttempt は d に送信結果を記録した配信を返す
func recordAttempt(t *testing.T, d core.Delivery, result core.AttemptResult, schedule core.RetrySchedule, now time.Time) core.Delivery {
	t.Helper()
	next, err := d.RecordAttempt(result, schedule, now, 0.5)
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	return next
}

// testAttempt は 503 を受け取った試行を返す
func testAttempt(now time.Time) core.Attempt {
	return core.Attempt{
		AttemptedAt: now,
		Outcome:     core.AttemptOutcomeRetryableFailure,
		StatusCode:  503,
		Duration:    1500 * time.Millisecond,
	}
}

func attemptRawItem(t *testing.T, client *dynamodb.Client, key core.DeliveryKey, seq int) map[string]types.AttributeValue {
	t.Helper()
	return getRawItem(t, client, testMessagesTable, messagePK(key.TenantID, key.MessageID), attemptSK(key.EndpointID, seq))
}

func TestMessageRepository_SaveAttempt(t *testing.T) {
	repo, client, clock := newTestMessageRepository(t)
	key := testDeliveryKey(testTenantID(t))
	if err := repo.CreateDelivery(t.Context(), key); err != nil {
		t.Fatalf("CreateDelivery: %v", err)
	}

	clock.advance(time.Hour)
	pending := core.NewDelivery()
	attempt := testAttempt(clock.now())
	retrying := recordAttempt(t, pending, core.AttemptResult{Outcome: attempt.Outcome, StatusCode: attempt.StatusCode}, core.DefaultRetrySchedule, clock.now())
	if err := repo.SaveAttempt(t.Context(), key, pending, retrying, attempt); err != nil {
		t.Fatalf("SaveAttempt: %v", err)
	}

	got, err := repo.GetDelivery(t.Context(), key)
	if err != nil {
		t.Fatalf("GetDelivery: %v", err)
	}
	if got.Status() != core.DeliveryStatusRetrying || got.AttemptCount() != 1 || got.AttemptSeq() != 1 {
		t.Errorf("Status, AttemptCount, AttemptSeq = %q, %d, %d, want retrying, 1, 1", got.Status(), got.AttemptCount(), got.AttemptSeq())
	}

	raw := attemptRawItem(t, client, key, 1)
	if raw == nil {
		t.Fatal("attempt item is nil")
	}
	if got := rawString(t, raw, "SK"); got != "ATTEMPT#ep_1#000001" {
		t.Errorf("SK = %q, want ATTEMPT#ep_1#000001", got)
	}
	if got := rawString(t, raw, "itemType"); got != "attempt" {
		t.Errorf("itemType = %q, want attempt", got)
	}
	if got, want := rawString(t, raw, "attemptedAt"), formatTime(attempt.AttemptedAt); got != want {
		t.Errorf("attemptedAt = %q, want %q", got, want)
	}
	if got := rawString(t, raw, "outcome"); got != "retryable_failure" {
		t.Errorf("outcome = %q, want retryable_failure", got)
	}
	if got := rawNumber(t, raw, "statusCode"); got != 503 {
		t.Errorf("statusCode = %d, want 503", got)
	}
	if got := rawNumber(t, raw, "durationMs"); got != 1500 {
		t.Errorf("durationMs = %d, want 1500", got)
	}
	if got, want := rawNumber(t, raw, "expiresAt"), clock.now().Add(testRetention).Unix(); got != want {
		t.Errorf("expiresAt = %d, want %d", got, want)
	}
}

func TestMessageRepository_SaveAttemptNoResponse(t *testing.T) {
	repo, client, clock := newTestMessageRepository(t)
	key := testDeliveryKey(testTenantID(t))
	if err := repo.CreateDelivery(t.Context(), key); err != nil {
		t.Fatalf("CreateDelivery: %v", err)
	}

	pending := core.NewDelivery()
	attempt := testAttempt(clock.now())
	attempt.StatusCode = 0
	retrying := recordAttempt(t, pending, core.AttemptResult{Outcome: attempt.Outcome}, core.DefaultRetrySchedule, clock.now())
	if err := repo.SaveAttempt(t.Context(), key, pending, retrying, attempt); err != nil {
		t.Fatalf("SaveAttempt: %v", err)
	}
	raw := attemptRawItem(t, client, key, 1)
	if raw == nil {
		t.Fatal("attempt item is nil")
	}
	if got := rawNumber(t, raw, "statusCode"); got != 0 {
		t.Errorf("statusCode = %d, want 0", got)
	}
}

func TestMessageRepository_SaveAttemptConflict(t *testing.T) {
	repo, client, clock := newTestMessageRepository(t)
	key := testDeliveryKey(testTenantID(t))
	if err := repo.CreateDelivery(t.Context(), key); err != nil {
		t.Fatalf("CreateDelivery: %v", err)
	}
	pending := core.NewDelivery()
	result := core.AttemptResult{Outcome: core.AttemptOutcomeRetryableFailure, StatusCode: 503}
	retrying := recordAttempt(t, pending, result, core.DefaultRetrySchedule, clock.now())
	if err := repo.SaveAttempt(t.Context(), key, pending, retrying, testAttempt(clock.now())); err != nil {
		t.Fatalf("first SaveAttempt: %v", err)
	}

	// 保存値は retrying なので、pending を前提にした書き込みは古い
	stale, err := core.RestoreDelivery(core.DeliveryStatusPending, 0, 1, time.Time{}, "")
	if err != nil {
		t.Fatalf("RestoreDelivery: %v", err)
	}
	next := recordAttempt(t, stale, result, core.DefaultRetrySchedule, clock.now())
	if next.AttemptSeq() != 2 {
		t.Fatalf("AttemptSeq = %d, want 2", next.AttemptSeq())
	}
	if err := repo.SaveAttempt(t.Context(), key, stale, next, testAttempt(clock.now())); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("second SaveAttempt err = %v, want ErrConflict", err)
	}

	got, err := repo.GetDelivery(t.Context(), key)
	if err != nil {
		t.Fatalf("GetDelivery: %v", err)
	}
	if got.Status() != core.DeliveryStatusRetrying || got.AttemptCount() != 1 || got.AttemptSeq() != 1 {
		t.Errorf("Status, AttemptCount, AttemptSeq = %q, %d, %d, want retrying, 1, 1", got.Status(), got.AttemptCount(), got.AttemptSeq())
	}
	if raw := attemptRawItem(t, client, key, 2); raw != nil {
		t.Errorf("attempt #000002 = %v, want nil", raw)
	}
}

func TestMessageRepository_SaveAttemptSeqCollision(t *testing.T) {
	repo, client, clock := newTestMessageRepository(t)
	key := testDeliveryKey(testTenantID(t))
	if err := repo.CreateDelivery(t.Context(), key); err != nil {
		t.Fatalf("CreateDelivery: %v", err)
	}
	putRawItem(t, client, testMessagesTable, map[string]types.AttributeValue{
		"PK":       &types.AttributeValueMemberS{Value: messagePK(key.TenantID, key.MessageID)},
		"SK":       &types.AttributeValueMemberS{Value: attemptSK(key.EndpointID, 1)},
		"itemType": &types.AttributeValueMemberS{Value: "attempt"},
	})

	pending := core.NewDelivery()
	retrying := recordAttempt(t, pending, core.AttemptResult{Outcome: core.AttemptOutcomeRetryableFailure, StatusCode: 503}, core.DefaultRetrySchedule, clock.now())
	if err := repo.SaveAttempt(t.Context(), key, pending, retrying, testAttempt(clock.now())); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("SaveAttempt err = %v, want ErrConflict", err)
	}

	got, err := repo.GetDelivery(t.Context(), key)
	if err != nil {
		t.Fatalf("GetDelivery: %v", err)
	}
	if got.Status() != core.DeliveryStatusPending || got.AttemptCount() != 0 || got.AttemptSeq() != 0 {
		t.Errorf("Status, AttemptCount, AttemptSeq = %q, %d, %d, want pending, 0, 0", got.Status(), got.AttemptCount(), got.AttemptSeq())
	}
}

func TestMessageRepository_ReprocessRoundTrip(t *testing.T) {
	repo, client, clock := newTestMessageRepository(t)
	key := testDeliveryKey(testTenantID(t))
	if err := repo.CreateDelivery(t.Context(), key); err != nil {
		t.Fatalf("CreateDelivery: %v", err)
	}
	createdExpiresAt := clock.now().Add(testRetention).Unix()

	// 送信は最大 2 回。2 回とも再送してよい失敗なら failed になる
	schedule := core.RetrySchedule{time.Second}
	result := core.AttemptResult{Outcome: core.AttemptOutcomeRetryableFailure, StatusCode: 503}
	d := core.NewDelivery()
	for i := range 2 {
		next := recordAttempt(t, d, result, schedule, clock.now())
		if err := repo.SaveAttempt(t.Context(), key, d, next, testAttempt(clock.now())); err != nil {
			t.Fatalf("SaveAttempt %d: %v", i+1, err)
		}
		d = next
		clock.advance(time.Second)
	}
	if d.Status() != core.DeliveryStatusFailed || d.FailureReason() != core.FailureReasonRetriesExhausted {
		t.Fatalf("Status, FailureReason = %q, %q, want failed, retries_exhausted", d.Status(), d.FailureReason())
	}
	first := attemptRawItem(t, client, key, 1)
	second := attemptRawItem(t, client, key, 2)
	if first == nil || second == nil {
		t.Fatalf("attempt items = %v, %v, want both", first, second)
	}

	clock.advance(time.Hour)
	reprocessed, err := d.Reprocess()
	if err != nil {
		t.Fatalf("Reprocess: %v", err)
	}
	if err := repo.SaveDelivery(t.Context(), key, d, reprocessed); err != nil {
		t.Fatalf("SaveDelivery: %v", err)
	}
	raw := getRawItem(t, client, testMessagesTable, messagePK(key.TenantID, key.MessageID), deliverySK(key.EndpointID))
	if _, ok := raw["failureReason"]; ok {
		t.Errorf("failureReason = %v, want removed", raw["failureReason"])
	}
	if _, ok := raw["nextAttemptAt"]; ok {
		t.Errorf("nextAttemptAt = %v, want removed", raw["nextAttemptAt"])
	}
	if got := rawNumber(t, raw, "attemptCount"); got != 0 {
		t.Errorf("attemptCount = %d, want 0", got)
	}
	if got := rawNumber(t, raw, "attemptSeq"); got != 2 {
		t.Errorf("attemptSeq = %d, want 2", got)
	}
	if got := rawNumber(t, raw, "expiresAt"); got != createdExpiresAt {
		t.Errorf("expiresAt = %d, want %d", got, createdExpiresAt)
	}

	next := recordAttempt(t, reprocessed, result, schedule, clock.now())
	if err := repo.SaveAttempt(t.Context(), key, reprocessed, next, testAttempt(clock.now())); err != nil {
		t.Fatalf("SaveAttempt after reprocess: %v", err)
	}
	if raw := attemptRawItem(t, client, key, 3); raw == nil {
		t.Error("attempt #000003 is nil")
	}
	if got := attemptRawItem(t, client, key, 1); !reflect.DeepEqual(got, first) {
		t.Errorf("attempt #000001 = %v, want unchanged %v", got, first)
	}
	if got := attemptRawItem(t, client, key, 2); !reflect.DeepEqual(got, second) {
		t.Errorf("attempt #000002 = %v, want unchanged %v", got, second)
	}
}
