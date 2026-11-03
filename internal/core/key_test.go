package core

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateID(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"有効", "t_1", false},
		{"空文字", "", true},
		{"区切り文字を含む", "t#1", true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateID("tenantId", test.value)
			if !test.wantErr {
				if err != nil {
					t.Fatalf("ValidateID(%q) = %v, want nil", test.value, err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("ValidateID(%q) = %v, want ErrInvalidKey", test.value, err)
			}
			if !strings.Contains(err.Error(), "tenantId") {
				t.Errorf("ValidateID(%q) のエラー %q に名前 tenantId が含まれない", test.value, err)
			}
		})
	}
}

func TestDeliveryKeyValidate(t *testing.T) {
	tests := []struct {
		name    string
		key     DeliveryKey
		wantErr bool
	}{
		{"有効", DeliveryKey{TenantID: "t_1", MessageID: "m_1", EndpointID: "ep_1"}, false},
		{"messageId にドット", DeliveryKey{TenantID: "t_1", MessageID: "m.1", EndpointID: "ep_1"}, false},
		{"tenantId が空", DeliveryKey{TenantID: "", MessageID: "m_1", EndpointID: "ep_1"}, true},
		{"messageId が空", DeliveryKey{TenantID: "t_1", MessageID: "", EndpointID: "ep_1"}, true},
		{"endpointId が空", DeliveryKey{TenantID: "t_1", MessageID: "m_1", EndpointID: ""}, true},
		{"tenantId に区切り文字", DeliveryKey{TenantID: "t_1#MSG#m_9", MessageID: "m_1", EndpointID: "ep_1"}, true},
		{"messageId に区切り文字", DeliveryKey{TenantID: "t_1", MessageID: "m_1#x", EndpointID: "ep_1"}, true},
		{"endpointId に区切り文字", DeliveryKey{TenantID: "t_1", MessageID: "m_1", EndpointID: "ep_1#x"}, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.key.Validate()
			if !test.wantErr {
				if err != nil {
					t.Fatalf("%+v.Validate() = %v, want nil", test.key, err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("%+v.Validate() = %v, want ErrInvalidKey", test.key, err)
			}
		})
	}
}
