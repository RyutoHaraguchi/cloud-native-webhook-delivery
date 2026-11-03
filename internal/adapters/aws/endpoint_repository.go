package aws

import (
	"context"
	"errors"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// EndpointRepositoryConfig は EndpointRepository の設定。値は cmd/ が環境変数から読んで渡す
type EndpointRepositoryConfig struct {
	// Table は config テーブルの名前
	Table string
}

// EndpointRepository は config テーブルのエンドポイントを読み書きする
type EndpointRepository struct {
	client *dynamodb.Client
	table  string
	// pageLimit は ListActive の Query の 1 ページの件数。0 なら Limit を指定しない。
	// ページをまたぐ読み出しを確かめるため、同じパッケージのテストだけが設定する
	pageLimit int32
}

var _ core.EndpointRepository = (*EndpointRepository)(nil)

// NewEndpointRepository は EndpointRepository を作る。テーブル名が空ならエラーを返す
func NewEndpointRepository(client *dynamodb.Client, cfg EndpointRepositoryConfig) (*EndpointRepository, error) {
	if cfg.Table == "" {
		return nil, errors.New("endpoint repository: table is empty")
	}
	return &EndpointRepository{client: client, table: cfg.Table}, nil
}

// endpointItem はエンドポイントの項目の属性。鍵（PK、SK）は含めない
type endpointItem struct {
	URL        string   `dynamodbav:"url"`
	EventTypes []string `dynamodbav:"eventTypes"`
	Secret     []byte   `dynamodbav:"secret"`
	Headers    []byte   `dynamodbav:"headers"`
	Status     string   `dynamodbav:"status"`
}

// endpointRequiredAttributes は読み出した項目に必ずある属性。headers は任意
var endpointRequiredAttributes = []string{"url", "eventTypes", "secret", "status"}

// decodeEndpoint は読み出した項目を検証し、Endpoint に変換する
func decodeEndpoint(tenantID, endpointID string, item map[string]types.AttributeValue) (core.Endpoint, error) {
	if err := requireAttributes(item, endpointRequiredAttributes...); err != nil {
		return core.Endpoint{}, fmt.Errorf("endpoint %s: %w", endpointID, err)
	}
	var v endpointItem
	if err := attributevalue.UnmarshalMap(item, &v); err != nil {
		return core.Endpoint{}, fmt.Errorf("endpoint %s: unmarshal: %w", endpointID, err)
	}
	e := core.Endpoint{
		TenantID:         tenantID,
		ID:               endpointID,
		URL:              v.URL,
		EventTypes:       v.EventTypes,
		EncryptedSecret:  v.Secret,
		EncryptedHeaders: v.Headers,
		Status:           core.EndpointStatus(v.Status),
	}
	if err := e.Validate(); err != nil {
		return core.Endpoint{}, fmt.Errorf("endpoint %s: %w", endpointID, err)
	}
	return e, nil
}

// endpointKey はエンドポイントの項目の鍵を返す
func endpointKey(tenantID, endpointID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: endpointPK(tenantID)},
		"SK": &types.AttributeValueMemberS{Value: endpointSK(endpointID)},
	}
}

// Get はエンドポイントを取得する。なければ ErrNotFound、不正な ID は ErrInvalidKey を返す
func (r *EndpointRepository) Get(ctx context.Context, tenantID, endpointID string) (core.Endpoint, error) {
	if err := core.ValidateID("tenantId", tenantID); err != nil {
		return core.Endpoint{}, fmt.Errorf("get endpoint: %w", err)
	}
	if err := core.ValidateID("endpointId", endpointID); err != nil {
		return core.Endpoint{}, fmt.Errorf("get endpoint: %w", err)
	}
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      awssdk.String(r.table),
		Key:            endpointKey(tenantID, endpointID),
		ConsistentRead: awssdk.Bool(true),
	})
	if err != nil {
		return core.Endpoint{}, fmt.Errorf("get endpoint %s: %w", endpointID, err)
	}
	if len(out.Item) == 0 {
		return core.Endpoint{}, fmt.Errorf("get endpoint %s: %w", endpointID, core.ErrNotFound)
	}
	e, err := decodeEndpoint(tenantID, endpointID, out.Item)
	if err != nil {
		return core.Endpoint{}, fmt.Errorf("get endpoint: %w", err)
	}
	return e, nil
}

// ListActive はテナントの active なエンドポイントを返す。
// 壊れた値が検証に届くよう、status では絞り込まずに全ページを読み、すべてを検証してから active を残す。
// 1 件でも壊れていれば、結果を返さずにエラーにする
func (r *EndpointRepository) ListActive(ctx context.Context, tenantID string) ([]core.Endpoint, error) {
	if err := core.ValidateID("tenantId", tenantID); err != nil {
		return nil, fmt.Errorf("list active endpoints: %w", err)
	}
	input := &dynamodb.QueryInput{
		TableName:              awssdk.String(r.table),
		KeyConditionExpression: awssdk.String("PK = :pk AND begins_with(SK, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":     &types.AttributeValueMemberS{Value: endpointPK(tenantID)},
			":prefix": &types.AttributeValueMemberS{Value: endpointSKPrefix},
		},
		ConsistentRead: awssdk.Bool(true),
	}
	if r.pageLimit > 0 {
		input.Limit = awssdk.Int32(r.pageLimit)
	}

	var active []core.Endpoint
	paginator := dynamodb.NewQueryPaginator(r.client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list active endpoints: %w", err)
		}
		for _, item := range page.Items {
			endpointID, err := endpointIDFromItem(item)
			if err != nil {
				return nil, fmt.Errorf("list active endpoints: %w", err)
			}
			e, err := decodeEndpoint(tenantID, endpointID, item)
			if err != nil {
				return nil, fmt.Errorf("list active endpoints: %w", err)
			}
			if e.Status == core.EndpointStatusActive {
				active = append(active, e)
			}
		}
	}
	return active, nil
}

// endpointIDFromItem は項目の SK からエンドポイント ID を取り出す
func endpointIDFromItem(item map[string]types.AttributeValue) (string, error) {
	sk, ok := item["SK"].(*types.AttributeValueMemberS)
	if !ok {
		return "", errors.New("endpoint item has no string SK")
	}
	endpointID, ok := strings.CutPrefix(sk.Value, endpointSKPrefix)
	if !ok {
		return "", fmt.Errorf("endpoint item SK %q has no prefix %q", sk.Value, endpointSKPrefix)
	}
	return endpointID, nil
}

// Disable はエンドポイントを disabled にする。削除済みなら何もせず nil を返す。
// 条件 attribute_exists(PK) で、存在しない項目を UpdateItem が新しく作らないようにする
func (r *EndpointRepository) Disable(ctx context.Context, tenantID, endpointID string) error {
	if err := core.ValidateID("tenantId", tenantID); err != nil {
		return fmt.Errorf("disable endpoint: %w", err)
	}
	if err := core.ValidateID("endpointId", endpointID); err != nil {
		return fmt.Errorf("disable endpoint: %w", err)
	}
	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                awssdk.String(r.table),
		Key:                      endpointKey(tenantID, endpointID),
		UpdateExpression:         awssdk.String("SET #status = :disabled"),
		ConditionExpression:      awssdk.String("attribute_exists(PK)"),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":disabled": &types.AttributeValueMemberS{Value: string(core.EndpointStatusDisabled)},
		},
	})
	if err != nil {
		// 条件の失敗は項目がない（削除済み）ことを表すので、何もせず成功とする
		if _, ok := errors.AsType[*types.ConditionalCheckFailedException](err); ok {
			return nil
		}
		return fmt.Errorf("disable endpoint %s: %w", endpointID, err)
	}
	return nil
}
