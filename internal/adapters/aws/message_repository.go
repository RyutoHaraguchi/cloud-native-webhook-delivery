package aws

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// 項目の種類を表す itemType の値。enqueuer は Streams のフィルタで message だけを受け取る
const (
	itemTypeMessage  = "message"
	itemTypeDelivery = "delivery"
	itemTypeAttempt  = "attempt"
)

var _ core.MessageRepository = (*MessageRepository)(nil)

// MessageRepositoryConfig は MessageRepository の設定。値は cmd/ が環境変数から読んで渡す
type MessageRepositoryConfig struct {
	// Table は messages テーブルの名前
	Table string
	// Retention は項目を最初に書いてから TTL で消すまでの期間
	Retention time.Duration
}

// MessageRepository は messages テーブルのメッセージと配信を読み書きする
type MessageRepository struct {
	client    *dynamodb.Client
	table     string
	retention time.Duration
	now       func() time.Time
}

// NewMessageRepository は MessageRepository を作る。
// テーブル名が空、保持期間が 0 以下、now が nil ならエラーを返す
func NewMessageRepository(client *dynamodb.Client, cfg MessageRepositoryConfig, now func() time.Time) (*MessageRepository, error) {
	if cfg.Table == "" {
		return nil, errors.New("message repository: table is empty")
	}
	if cfg.Retention <= 0 {
		return nil, fmt.Errorf("message repository: retention %s is not positive", cfg.Retention)
	}
	if now == nil {
		return nil, errors.New("message repository: now is nil")
	}
	return &MessageRepository{client: client, table: cfg.Table, retention: cfg.Retention, now: now}, nil
}

// messageItem はメッセージの項目の属性。鍵（PK、SK）は含めない
type messageItem struct {
	ItemType  string `dynamodbav:"itemType"`
	Type      string `dynamodbav:"type"`
	Payload   []byte `dynamodbav:"payload"`
	CreatedAt string `dynamodbav:"createdAt"`
	ExpiresAt int64  `dynamodbav:"expiresAt"`
}

// messageRequiredAttributes は読み出したメッセージの項目に必ずある属性
var messageRequiredAttributes = []string{"type", "payload", "createdAt"}

// deliveryItem は配信の項目の属性。鍵（PK、SK）は含めない。
// nextAttemptAt と failureReason は値がなければ属性ごと書かない
type deliveryItem struct {
	ItemType      string `dynamodbav:"itemType"`
	Status        string `dynamodbav:"status"`
	AttemptCount  int    `dynamodbav:"attemptCount"`
	AttemptSeq    int    `dynamodbav:"attemptSeq"`
	NextAttemptAt string `dynamodbav:"nextAttemptAt,omitempty"`
	FailureReason string `dynamodbav:"failureReason,omitempty"`
	ExpiresAt     int64  `dynamodbav:"expiresAt"`
}

// deliveryRequiredAttributes は読み出した配信の項目に必ずある属性。nextAttemptAt と failureReason は任意
var deliveryRequiredAttributes = []string{"status", "attemptCount", "attemptSeq"}

// attemptItem は試行の項目の属性。鍵（PK、SK）は含めない。statusCode は応答がなければ 0
type attemptItem struct {
	ItemType    string `dynamodbav:"itemType"`
	AttemptedAt string `dynamodbav:"attemptedAt"`
	Outcome     string `dynamodbav:"outcome"`
	StatusCode  int    `dynamodbav:"statusCode"`
	DurationMs  int64  `dynamodbav:"durationMs"`
	ExpiresAt   int64  `dynamodbav:"expiresAt"`
}

// itemKey は messages テーブルの項目の鍵を返す
func itemKey(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: pk},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}
}

// deliveryKeyAttributes は配信の項目の鍵を返す
func deliveryKeyAttributes(key core.DeliveryKey) map[string]types.AttributeValue {
	return itemKey(messagePK(key.TenantID, key.MessageID), deliverySK(key.EndpointID))
}

// expiresAt は今から保持期間が過ぎた時刻を、TTL に使うエポック秒で返す
func (r *MessageRepository) expiresAt() int64 {
	return r.now().Add(r.retention).Unix()
}

// putIfAbsent は鍵を足した項目を、同じ鍵の項目がないときだけ書く。
// 既にあれば ConditionalCheckFailedException を含むエラーを返す
func (r *MessageRepository) putIfAbsent(ctx context.Context, key, attrs map[string]types.AttributeValue) error {
	maps.Copy(attrs, key)
	_, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           awssdk.String(r.table),
		Item:                attrs,
		ConditionExpression: awssdk.String("attribute_not_exists(PK)"),
	})
	return err
}

// CreateMessage はメッセージを保存する。同じ ID のメッセージが既にあれば ErrConflict を返す
func (r *MessageRepository) CreateMessage(ctx context.Context, m core.Message) error {
	if err := m.Validate(); err != nil {
		return fmt.Errorf("create message: %w", err)
	}
	attrs, err := attributevalue.MarshalMap(messageItem{
		ItemType:  itemTypeMessage,
		Type:      m.Type,
		Payload:   m.Payload,
		CreatedAt: formatTime(m.CreatedAt),
		ExpiresAt: r.expiresAt(),
	})
	if err != nil {
		return fmt.Errorf("create message %s: marshal: %w", m.ID, err)
	}
	if err := r.putIfAbsent(ctx, itemKey(messagePK(m.TenantID, m.ID), messageSK), attrs); err != nil {
		if _, ok := errors.AsType[*types.ConditionalCheckFailedException](err); ok {
			return fmt.Errorf("create message %s: %w", m.ID, core.ErrConflict)
		}
		return fmt.Errorf("create message %s: %w", m.ID, err)
	}
	return nil
}

// GetMessage はメッセージを取得する。なければ ErrNotFound、不正な ID は ErrInvalidKey を返す
func (r *MessageRepository) GetMessage(ctx context.Context, tenantID, messageID string) (core.Message, error) {
	if err := core.ValidateID("tenantId", tenantID); err != nil {
		return core.Message{}, fmt.Errorf("get message: %w", err)
	}
	if err := core.ValidateID("messageId", messageID); err != nil {
		return core.Message{}, fmt.Errorf("get message: %w", err)
	}
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      awssdk.String(r.table),
		Key:            itemKey(messagePK(tenantID, messageID), messageSK),
		ConsistentRead: awssdk.Bool(true),
	})
	if err != nil {
		return core.Message{}, fmt.Errorf("get message %s: %w", messageID, err)
	}
	if len(out.Item) == 0 {
		return core.Message{}, fmt.Errorf("get message %s: %w", messageID, core.ErrNotFound)
	}
	m, err := decodeMessage(tenantID, messageID, out.Item)
	if err != nil {
		return core.Message{}, fmt.Errorf("get message: %w", err)
	}
	return m, nil
}

// decodeMessage は読み出した項目を検証し、Message に変換する
func decodeMessage(tenantID, messageID string, item map[string]types.AttributeValue) (core.Message, error) {
	if err := requireAttributes(item, messageRequiredAttributes...); err != nil {
		return core.Message{}, fmt.Errorf("message %s: %w", messageID, err)
	}
	var v messageItem
	if err := attributevalue.UnmarshalMap(item, &v); err != nil {
		return core.Message{}, fmt.Errorf("message %s: unmarshal: %w", messageID, err)
	}
	createdAt, err := parseTime(v.CreatedAt)
	if err != nil {
		return core.Message{}, fmt.Errorf("message %s: createdAt: %w", messageID, err)
	}
	m := core.Message{
		TenantID:  tenantID,
		ID:        messageID,
		Type:      v.Type,
		Payload:   v.Payload,
		CreatedAt: createdAt,
	}
	if err := m.Validate(); err != nil {
		return core.Message{}, fmt.Errorf("message %s: %w", messageID, err)
	}
	return m, nil
}

// CreateDelivery は作成直後の配信を保存する。既にあれば何もせず nil を返す。不正な鍵は ErrInvalidKey を返す
func (r *MessageRepository) CreateDelivery(ctx context.Context, key core.DeliveryKey) error {
	if err := key.Validate(); err != nil {
		return fmt.Errorf("create delivery: %w", err)
	}
	d := core.NewDelivery()
	attrs, err := attributevalue.MarshalMap(deliveryItem{
		ItemType:     itemTypeDelivery,
		Status:       string(d.Status()),
		AttemptCount: d.AttemptCount(),
		AttemptSeq:   d.AttemptSeq(),
		ExpiresAt:    r.expiresAt(),
	})
	if err != nil {
		return fmt.Errorf("create delivery %s: marshal: %w", key.EndpointID, err)
	}
	if err := r.putIfAbsent(ctx, deliveryKeyAttributes(key), attrs); err != nil {
		// 条件の失敗は配信が既にあることを表す。enqueuer の再実行で起きるので、何もせず成功とする
		if _, ok := errors.AsType[*types.ConditionalCheckFailedException](err); ok {
			return nil
		}
		return fmt.Errorf("create delivery %s: %w", key.EndpointID, err)
	}
	return nil
}

// GetDelivery は配信を取得する。なければ ErrNotFound、不正な鍵は ErrInvalidKey を返す
func (r *MessageRepository) GetDelivery(ctx context.Context, key core.DeliveryKey) (core.Delivery, error) {
	if err := key.Validate(); err != nil {
		return core.Delivery{}, fmt.Errorf("get delivery: %w", err)
	}
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      awssdk.String(r.table),
		Key:            deliveryKeyAttributes(key),
		ConsistentRead: awssdk.Bool(true),
	})
	if err != nil {
		return core.Delivery{}, fmt.Errorf("get delivery %s: %w", key.EndpointID, err)
	}
	if len(out.Item) == 0 {
		return core.Delivery{}, fmt.Errorf("get delivery %s: %w", key.EndpointID, core.ErrNotFound)
	}
	d, err := decodeDelivery(key.EndpointID, out.Item)
	if err != nil {
		return core.Delivery{}, fmt.Errorf("get delivery: %w", err)
	}
	return d, nil
}

// decodeDelivery は読み出した項目を検証し、Delivery に復元する
func decodeDelivery(endpointID string, item map[string]types.AttributeValue) (core.Delivery, error) {
	if err := requireAttributes(item, deliveryRequiredAttributes...); err != nil {
		return core.Delivery{}, fmt.Errorf("delivery %s: %w", endpointID, err)
	}
	var v deliveryItem
	if err := attributevalue.UnmarshalMap(item, &v); err != nil {
		return core.Delivery{}, fmt.Errorf("delivery %s: unmarshal: %w", endpointID, err)
	}
	var nextAttemptAt time.Time
	if v.NextAttemptAt != "" {
		t, err := parseTime(v.NextAttemptAt)
		if err != nil {
			return core.Delivery{}, fmt.Errorf("delivery %s: nextAttemptAt: %w", endpointID, err)
		}
		nextAttemptAt = t
	}
	d, err := core.RestoreDelivery(core.DeliveryStatus(v.Status), v.AttemptCount, v.AttemptSeq, nextAttemptAt, core.FailureReason(v.FailureReason))
	if err != nil {
		return core.Delivery{}, fmt.Errorf("delivery %s: %w", endpointID, err)
	}
	return d, nil
}

// SaveDelivery は prev の Status と AttemptCount が保存値と一致するときだけ next を書く。
// 一致しない、または項目がなければ ErrConflict、不正な鍵は ErrInvalidKey を返す
func (r *MessageRepository) SaveDelivery(ctx context.Context, key core.DeliveryKey, prev, next core.Delivery) error {
	if err := key.Validate(); err != nil {
		return fmt.Errorf("save delivery: %w", err)
	}
	u := deliveryUpdate(r.table, key, prev, next)
	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 u.TableName,
		Key:                       u.Key,
		UpdateExpression:          u.UpdateExpression,
		ConditionExpression:       u.ConditionExpression,
		ExpressionAttributeNames:  u.ExpressionAttributeNames,
		ExpressionAttributeValues: u.ExpressionAttributeValues,
	})
	if err != nil {
		if _, ok := errors.AsType[*types.ConditionalCheckFailedException](err); ok {
			return fmt.Errorf("save delivery %s: %w", key.EndpointID, core.ErrConflict)
		}
		return fmt.Errorf("save delivery %s: %w", key.EndpointID, err)
	}
	return nil
}

// deliveryUpdate は配信を prev から next に書き換える更新を返す。SaveDelivery と SaveAttempt の両方で使う。
// 条件は項目があり、status と attemptCount が prev と一致すること。
// nextAttemptAt と failureReason は値がなければ属性を消す。expiresAt は最初に書いた値のまま延ばさない
func deliveryUpdate(table string, key core.DeliveryKey, prev, next core.Delivery) *types.Update {
	set := []string{"#status = :status", "attemptCount = :attemptCount", "attemptSeq = :attemptSeq"}
	var remove []string
	values := map[string]types.AttributeValue{
		":status":       &types.AttributeValueMemberS{Value: string(next.Status())},
		":attemptCount": &types.AttributeValueMemberN{Value: strconv.Itoa(next.AttemptCount())},
		":attemptSeq":   &types.AttributeValueMemberN{Value: strconv.Itoa(next.AttemptSeq())},
		":prevStatus":   &types.AttributeValueMemberS{Value: string(prev.Status())},
		":prevCount":    &types.AttributeValueMemberN{Value: strconv.Itoa(prev.AttemptCount())},
	}

	if t := next.NextAttemptAt(); !t.IsZero() {
		set = append(set, "nextAttemptAt = :nextAttemptAt")
		values[":nextAttemptAt"] = &types.AttributeValueMemberS{Value: formatTime(t)}
	} else {
		remove = append(remove, "nextAttemptAt")
	}
	if reason := next.FailureReason(); reason != "" {
		set = append(set, "failureReason = :failureReason")
		values[":failureReason"] = &types.AttributeValueMemberS{Value: string(reason)}
	} else {
		remove = append(remove, "failureReason")
	}

	expr := "SET " + strings.Join(set, ", ")
	if len(remove) > 0 {
		expr += " REMOVE " + strings.Join(remove, ", ")
	}
	return &types.Update{
		TableName:                 awssdk.String(table),
		Key:                       deliveryKeyAttributes(key),
		UpdateExpression:          awssdk.String(expr),
		ConditionExpression:       awssdk.String("attribute_exists(PK) AND #status = :prevStatus AND attemptCount = :prevCount"),
		ExpressionAttributeNames:  map[string]string{"#status": "status"},
		ExpressionAttributeValues: values,
	}
}

// SaveAttempt は SaveDelivery と同じ条件で next を書き、同時に試行の記録を書く。
// 配信と試行は 1 つのトランザクションで書くので、一方だけ書かれることはない。
// 試行の通し番号は next.AttemptSeq() を使う。条件が合わない、または同じ番号の試行が既にあれば ErrConflict、
// 不正な鍵は ErrInvalidKey を返す
func (r *MessageRepository) SaveAttempt(ctx context.Context, key core.DeliveryKey, prev, next core.Delivery, attempt core.Attempt) error {
	if err := key.Validate(); err != nil {
		return fmt.Errorf("save attempt: %w", err)
	}
	seq := next.AttemptSeq()
	attrs, err := attributevalue.MarshalMap(attemptItem{
		ItemType:    itemTypeAttempt,
		AttemptedAt: formatTime(attempt.AttemptedAt),
		Outcome:     string(attempt.Outcome),
		StatusCode:  attempt.StatusCode,
		DurationMs:  attempt.Duration.Milliseconds(),
		ExpiresAt:   r.expiresAt(),
	})
	if err != nil {
		return fmt.Errorf("save attempt %s#%d: marshal: %w", key.EndpointID, seq, err)
	}
	maps.Copy(attrs, itemKey(messagePK(key.TenantID, key.MessageID), attemptSK(key.EndpointID, seq)))

	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Update: deliveryUpdate(r.table, key, prev, next)},
			{Put: &types.Put{
				TableName:           awssdk.String(r.table),
				Item:                attrs,
				ConditionExpression: awssdk.String("attribute_not_exists(PK)"),
			}},
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return fmt.Errorf("save attempt %s#%d: %w", key.EndpointID, seq, core.ErrConflict)
		}
		// 条件の失敗以外（TransactionConflict を含む）は ErrConflict にせず wrap して返し、再受信でやり直す
		return fmt.Errorf("save attempt %s#%d: %w", key.EndpointID, seq, err)
	}
	return nil
}

// isConditionalCheckFailed は、トランザクションの取り消し理由に条件の失敗が含まれるかを返す
func isConditionalCheckFailed(err error) bool {
	canceled, ok := errors.AsType[*types.TransactionCanceledException](err)
	if !ok {
		return false
	}
	for _, r := range canceled.CancellationReasons {
		if awssdk.ToString(r.Code) == "ConditionalCheckFailed" {
			return true
		}
	}
	return false
}
