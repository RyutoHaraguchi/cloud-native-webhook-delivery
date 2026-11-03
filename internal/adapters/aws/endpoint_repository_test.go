//go:build integration

package aws

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// testConfigTable は compose が作る config テーブルの名前
const testConfigTable = "config"

func newTestEndpointRepository(t *testing.T) (*EndpointRepository, *dynamodb.Client) {
	t.Helper()
	client := testDynamoDB(t)
	repo, err := NewEndpointRepository(client, EndpointRepositoryConfig{Table: testConfigTable})
	if err != nil {
		t.Fatalf("NewEndpointRepository: %v", err)
	}
	return repo, client
}

// endpointRawItem はエンドポイントの保存値を全属性つきで作る。テストごとに属性を消したり差し替えたりして使う
func endpointRawItem(tenantID, endpointID, status string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK":     &types.AttributeValueMemberS{Value: endpointPK(tenantID)},
		"SK":     &types.AttributeValueMemberS{Value: endpointSK(endpointID)},
		"url":    &types.AttributeValueMemberS{Value: "https://example.com/hooks/" + endpointID},
		"status": &types.AttributeValueMemberS{Value: status},
		"secret": &types.AttributeValueMemberB{Value: []byte("encrypted-secret")},
		"eventTypes": &types.AttributeValueMemberL{Value: []types.AttributeValue{
			&types.AttributeValueMemberS{Value: "order.created"},
			&types.AttributeValueMemberS{Value: "order.updated"},
		}},
		"headers": &types.AttributeValueMemberB{Value: []byte("encrypted-headers")},
	}
}

func endpointIDs(endpoints []core.Endpoint) []string {
	ids := make([]string, 0, len(endpoints))
	for _, e := range endpoints {
		ids = append(ids, e.ID)
	}
	slices.Sort(ids)
	return ids
}

func TestEndpointRepository_Get(t *testing.T) {
	repo, client := newTestEndpointRepository(t)
	tenantID := testTenantID(t)
	putRawItem(t, client, testConfigTable, endpointRawItem(tenantID, "ep_1", "active"))

	got, err := repo.Get(t.Context(), tenantID, "ep_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TenantID != tenantID || got.ID != "ep_1" {
		t.Errorf("TenantID, ID = %q, %q, want %q, %q", got.TenantID, got.ID, tenantID, "ep_1")
	}
	if got.URL != "https://example.com/hooks/ep_1" {
		t.Errorf("URL = %q", got.URL)
	}
	if !slices.Equal(got.EventTypes, []string{"order.created", "order.updated"}) {
		t.Errorf("EventTypes = %v", got.EventTypes)
	}
	if string(got.EncryptedSecret) != "encrypted-secret" {
		t.Errorf("EncryptedSecret = %q", got.EncryptedSecret)
	}
	if string(got.EncryptedHeaders) != "encrypted-headers" {
		t.Errorf("EncryptedHeaders = %q", got.EncryptedHeaders)
	}
	if got.Status != core.EndpointStatusActive {
		t.Errorf("Status = %q, want %q", got.Status, core.EndpointStatusActive)
	}
}

func TestEndpointRepository_GetNotFound(t *testing.T) {
	repo, _ := newTestEndpointRepository(t)

	_, err := repo.Get(t.Context(), testTenantID(t), "ep_missing")
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestEndpointRepository_GetMissingAttribute(t *testing.T) {
	repo, client := newTestEndpointRepository(t)
	tenantID := testTenantID(t)
	item := endpointRawItem(tenantID, "ep_1", "active")
	delete(item, "secret")
	putRawItem(t, client, testConfigTable, item)

	_, err := repo.Get(t.Context(), tenantID, "ep_1")
	if err == nil {
		t.Fatal("err = nil, want error")
	}
	if errors.Is(err, core.ErrNotFound) {
		t.Errorf("err = %v, should not be ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Errorf("err = %v, want it to name secret", err)
	}
}

func TestEndpointRepository_GetInvalidStatus(t *testing.T) {
	repo, client := newTestEndpointRepository(t)
	tenantID := testTenantID(t)
	putRawItem(t, client, testConfigTable, endpointRawItem(tenantID, "ep_1", "disabeld"))

	_, err := repo.Get(t.Context(), tenantID, "ep_1")
	if !errors.Is(err, core.ErrInvalidEndpoint) {
		t.Fatalf("err = %v, want ErrInvalidEndpoint", err)
	}
}

func TestEndpointRepository_EventTypes(t *testing.T) {
	repo, client := newTestEndpointRepository(t)
	tenantID := testTenantID(t)

	empty := endpointRawItem(tenantID, "ep_empty", "active")
	empty["eventTypes"] = &types.AttributeValueMemberL{Value: []types.AttributeValue{}}
	putRawItem(t, client, testConfigTable, empty)

	got, err := repo.Get(t.Context(), tenantID, "ep_empty")
	if err != nil {
		t.Fatalf("Get (空の eventTypes): %v", err)
	}
	if len(got.EventTypes) != 0 {
		t.Errorf("EventTypes = %v, want empty", got.EventTypes)
	}

	null := endpointRawItem(tenantID, "ep_null", "active")
	null["eventTypes"] = &types.AttributeValueMemberNULL{Value: true}
	putRawItem(t, client, testConfigTable, null)

	if _, err := repo.Get(t.Context(), tenantID, "ep_null"); err == nil {
		t.Fatal("Get (NULL の eventTypes): err = nil, want error")
	}
}

func TestEndpointRepository_ListActive(t *testing.T) {
	repo, client := newTestEndpointRepository(t)
	tenantID := testTenantID(t)
	otherTenantID := testTenantID(t)

	putRawItem(t, client, testConfigTable, endpointRawItem(tenantID, "ep_1", "active"))
	putRawItem(t, client, testConfigTable, endpointRawItem(tenantID, "ep_2", "active"))
	putRawItem(t, client, testConfigTable, endpointRawItem(tenantID, "ep_3", "disabled"))
	// エンドポイントでない同じテナントの項目。エンドポイントの属性を持たない
	putRawItem(t, client, testConfigTable, map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: endpointPK(tenantID)},
		"SK": &types.AttributeValueMemberS{Value: "META"},
	})
	putRawItem(t, client, testConfigTable, map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: endpointPK(tenantID)},
		"SK": &types.AttributeValueMemberS{Value: "APIKEY#k_1"},
	})
	putRawItem(t, client, testConfigTable, endpointRawItem(otherTenantID, "ep_other", "active"))

	got, err := repo.ListActive(t.Context(), tenantID)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if ids := endpointIDs(got); !slices.Equal(ids, []string{"ep_1", "ep_2"}) {
		t.Errorf("IDs = %v, want [ep_1 ep_2]", ids)
	}
	for _, e := range got {
		if e.TenantID != tenantID || e.Status != core.EndpointStatusActive {
			t.Errorf("endpoint %s: TenantID = %q, Status = %q", e.ID, e.TenantID, e.Status)
		}
	}
}

func TestEndpointRepository_ListActivePages(t *testing.T) {
	repo, client := newTestEndpointRepository(t)
	repo.pageLimit = 1
	tenantID := testTenantID(t)
	for _, id := range []string{"ep_1", "ep_2", "ep_3"} {
		putRawItem(t, client, testConfigTable, endpointRawItem(tenantID, id, "active"))
	}

	got, err := repo.ListActive(t.Context(), tenantID)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if ids := endpointIDs(got); !slices.Equal(ids, []string{"ep_1", "ep_2", "ep_3"}) {
		t.Errorf("IDs = %v, want [ep_1 ep_2 ep_3]", ids)
	}

	// SK の順で最後のページに来る壊れた項目も検証に届くこと
	putRawItem(t, client, testConfigTable, endpointRawItem(tenantID, "zz", "disabeld"))
	got, err = repo.ListActive(t.Context(), tenantID)
	if err == nil {
		t.Fatal("err = nil, want error")
	}
	if got != nil {
		t.Errorf("result = %v, want nil", got)
	}
}

func TestEndpointRepository_ListActiveCorrupt(t *testing.T) {
	repo, client := newTestEndpointRepository(t)
	tenantID := testTenantID(t)
	putRawItem(t, client, testConfigTable, endpointRawItem(tenantID, "ep_1", "active"))
	putRawItem(t, client, testConfigTable, endpointRawItem(tenantID, "ep_2", "active"))
	putRawItem(t, client, testConfigTable, endpointRawItem(tenantID, "ep_3", "disabeld"))

	got, err := repo.ListActive(t.Context(), tenantID)
	if !errors.Is(err, core.ErrInvalidEndpoint) {
		t.Fatalf("err = %v, want ErrInvalidEndpoint", err)
	}
	if got != nil {
		t.Errorf("result = %v, want nil", got)
	}
}

func TestEndpointRepository_Disable(t *testing.T) {
	repo, client := newTestEndpointRepository(t)
	tenantID := testTenantID(t)
	putRawItem(t, client, testConfigTable, endpointRawItem(tenantID, "ep_1", "active"))

	if err := repo.Disable(t.Context(), tenantID, "ep_1"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	got, err := repo.Get(t.Context(), tenantID, "ep_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != core.EndpointStatusDisabled {
		t.Errorf("Status = %q, want %q", got.Status, core.EndpointStatusDisabled)
	}

	// 削除済み（存在しない）エンドポイントは何もせず nil。アイテムを作らない
	if err := repo.Disable(t.Context(), tenantID, "ep_missing"); err != nil {
		t.Fatalf("Disable (存在しない ID): %v", err)
	}
	if item := getRawItem(t, client, testConfigTable, endpointPK(tenantID), endpointSK("ep_missing")); item != nil {
		t.Errorf("item = %v, want nil", item)
	}
}

func TestEndpointRepository_InvalidID(t *testing.T) {
	repo, _ := newTestEndpointRepository(t)
	const tenantID = "t#x"

	if _, err := repo.Get(t.Context(), tenantID, "ep_1"); !errors.Is(err, core.ErrInvalidKey) {
		t.Errorf("Get: err = %v, want ErrInvalidKey", err)
	}
	if _, err := repo.ListActive(t.Context(), tenantID); !errors.Is(err, core.ErrInvalidKey) {
		t.Errorf("ListActive: err = %v, want ErrInvalidKey", err)
	}
	if err := repo.Disable(t.Context(), tenantID, "ep_1"); !errors.Is(err, core.ErrInvalidKey) {
		t.Errorf("Disable: err = %v, want ErrInvalidKey", err)
	}
}
