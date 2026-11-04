package aws

import (
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

func validEndpoint() core.Endpoint {
	return core.Endpoint{
		TenantID:        "t_1",
		ID:              "ep_1",
		URL:             "https://example.com/hooks",
		EventTypes:      []string{"order.created", "order.updated"},
		EncryptedSecret: []byte("encrypted-secret"),
		Status:          core.EndpointStatusActive,
	}
}

func TestEncodeEndpoint(t *testing.T) {
	tests := []struct {
		name        string
		modify      func(e *core.Endpoint)
		wantHeaders bool
		// want は decodeEndpoint で読み戻したときに期待する値。nil なら元の値と同じ
		want func(e core.Endpoint) core.Endpoint
	}{
		{
			name:   "eventTypes nil",
			modify: func(e *core.Endpoint) { e.EventTypes = nil },
			// nil の eventTypes は空の L で書くので、空のスライスとして読み戻る
			want: func(e core.Endpoint) core.Endpoint { e.EventTypes = []string{}; return e },
		},
		{
			name:   "headers nil",
			modify: func(e *core.Endpoint) { e.EncryptedHeaders = nil },
		},
		{
			name:   "headers empty",
			modify: func(e *core.Endpoint) { e.EncryptedHeaders = []byte{} },
			// 空の headers は属性ごと省くので、nil として読み戻る
			want: func(e core.Endpoint) core.Endpoint { e.EncryptedHeaders = nil; return e },
		},
		{
			name:        "headers present",
			modify:      func(e *core.Endpoint) { e.EncryptedHeaders = []byte("encrypted-headers") },
			wantHeaders: true,
		},
		{
			name:   "disabled",
			modify: func(e *core.Endpoint) { e.Status = core.EndpointStatusDisabled },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEndpoint()
			tt.modify(&e)

			item := encodeEndpoint(e)

			pk, ok := item["PK"].(*types.AttributeValueMemberS)
			if !ok || pk.Value != endpointPK(e.TenantID) {
				t.Errorf("PK = %#v, want S %q", item["PK"], endpointPK(e.TenantID))
			}
			sk, ok := item["SK"].(*types.AttributeValueMemberS)
			if !ok || sk.Value != endpointSK(e.ID) {
				t.Errorf("SK = %#v, want S %q", item["SK"], endpointSK(e.ID))
			}
			if _, ok := item["url"].(*types.AttributeValueMemberS); !ok {
				t.Errorf("url = %#v, want S", item["url"])
			}
			eventTypes, ok := item["eventTypes"].(*types.AttributeValueMemberL)
			if !ok {
				t.Fatalf("eventTypes = %#v, want L", item["eventTypes"])
			}
			if len(eventTypes.Value) != len(e.EventTypes) {
				t.Errorf("len(eventTypes) = %d, want %d", len(eventTypes.Value), len(e.EventTypes))
			}
			if _, ok := item["secret"].(*types.AttributeValueMemberB); !ok {
				t.Errorf("secret = %#v, want B", item["secret"])
			}
			if _, ok := item["status"].(*types.AttributeValueMemberS); !ok {
				t.Errorf("status = %#v, want S", item["status"])
			}
			headers, hasHeaders := item["headers"]
			if hasHeaders != tt.wantHeaders {
				t.Errorf("headers present = %v, want %v (%#v)", hasHeaders, tt.wantHeaders, headers)
			}
			if hasHeaders {
				if _, ok := headers.(*types.AttributeValueMemberB); !ok {
					t.Errorf("headers = %#v, want B", headers)
				}
			}
			if _, ok := item["itemType"]; ok {
				t.Errorf("itemType = %#v, want absent", item["itemType"])
			}

			got, err := decodeEndpoint(e.TenantID, e.ID, item)
			if err != nil {
				t.Fatalf("decodeEndpoint: %v", err)
			}
			want := e
			if tt.want != nil {
				want = tt.want(e)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("decodeEndpoint = %#v, want %#v", got, want)
			}
		})
	}
}
