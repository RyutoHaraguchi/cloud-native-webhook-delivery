package core

import (
	"errors"
	"testing"
)

func TestSecretContext_Validate(t *testing.T) {
	tests := []struct {
		name    string
		sc      SecretContext
		wantErr bool
	}{
		{"有効", SecretContext{TenantID: "t_a", EndpointID: "ep_1"}, false},
		{"テナント ID が空", SecretContext{TenantID: "", EndpointID: "ep_1"}, true},
		{"エンドポイント ID が空", SecretContext{TenantID: "t_a", EndpointID: ""}, true},
		{"テナント ID に区切り文字", SecretContext{TenantID: "t#a", EndpointID: "ep_1"}, true},
		{"エンドポイント ID に区切り文字", SecretContext{TenantID: "t_a", EndpointID: "ep#1"}, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.sc.Validate()
			if !test.wantErr {
				if err != nil {
					t.Fatalf("%+v.Validate() = %v, want nil", test.sc, err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("%+v.Validate() = %v, want ErrInvalidKey", test.sc, err)
			}
		})
	}
}
