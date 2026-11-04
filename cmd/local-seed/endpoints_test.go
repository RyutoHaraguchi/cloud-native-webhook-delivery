package main

import (
	"crypto/rand"
	"errors"
	"slices"
	"testing"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/adapters/local"
	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

func TestSeedEndpoints(t *testing.T) {
	cipher, err := local.NewCipher(testKeyBytes, rand.Reader)
	if err != nil {
		t.Fatalf("NewCipher() error = %v", err)
	}
	endpoints, err := seedEndpoints(t.Context(), cipher, testSigningSecret)
	if err != nil {
		t.Fatalf("seedEndpoints() error = %v", err)
	}

	// docs/architecture.md の「ローカルのシード」の表
	want := []core.Endpoint{
		{TenantID: "t_local", ID: "ep_ok", URL: "http://dummy-receiver:8080/ok", EventTypes: []string{"test.ok"}, Status: core.EndpointStatusActive},
		{TenantID: "t_local", ID: "ep_flaky", URL: "http://dummy-receiver:8080/flaky?fail=2&status=503", EventTypes: []string{"test.retry"}, Status: core.EndpointStatusActive},
		{TenantID: "t_local", ID: "ep_disabled", URL: "http://dummy-receiver:8080/disabled", EventTypes: []string{"test.ok"}, Status: core.EndpointStatusDisabled},
	}
	if len(endpoints) != len(want) {
		t.Fatalf("len(endpoints) = %d, want %d", len(endpoints), len(want))
	}
	for i, got := range endpoints {
		w := want[i]
		if got.TenantID != w.TenantID || got.ID != w.ID || got.URL != w.URL || got.Status != w.Status {
			t.Errorf("endpoints[%d] = {%s %s %s %s}, want {%s %s %s %s}",
				i, got.TenantID, got.ID, got.URL, got.Status, w.TenantID, w.ID, w.URL, w.Status)
		}
		if !slices.Equal(got.EventTypes, w.EventTypes) {
			t.Errorf("endpoints[%d].EventTypes = %v, want %v", i, got.EventTypes, w.EventTypes)
		}
		if got.EncryptedHeaders != nil {
			t.Errorf("endpoints[%d].EncryptedHeaders = %v, want nil", i, got.EncryptedHeaders)
		}
		if err := got.Validate(); err != nil {
			t.Errorf("endpoints[%d].Validate() error = %v", i, err)
		}

		plaintext, err := cipher.Decrypt(t.Context(), core.SecretContext{TenantID: got.TenantID, EndpointID: got.ID}, got.EncryptedSecret)
		if err != nil {
			t.Fatalf("Decrypt(%s) error = %v", got.ID, err)
		}
		if string(plaintext) != testSigningSecret {
			t.Errorf("Decrypt(%s) does not return the signing secret", got.ID)
		}

		// 別のエンドポイントのコンテキストでは復号できない
		other := endpoints[(i+1)%len(endpoints)].ID
		_, err = cipher.Decrypt(t.Context(), core.SecretContext{TenantID: got.TenantID, EndpointID: other}, got.EncryptedSecret)
		if !errors.Is(err, core.ErrUndecryptable) {
			t.Errorf("Decrypt(%s) with context of %s error = %v, want ErrUndecryptable", got.ID, other, err)
		}
	}
}
