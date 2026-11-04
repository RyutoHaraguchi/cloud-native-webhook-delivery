package main

import (
	"context"
	"fmt"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// seedTenantID はシードが書くテナント
const seedTenantID = "t_local"

// seedEndpoints は docs/architecture.md の「ローカルのシード」の表のエンドポイントを返す。
// シークレットはエンドポイントごとのコンテキストで暗号化する。固定ヘッダーは持たない
func seedEndpoints(ctx context.Context, c core.SecretCipher, signingSecret string) ([]core.Endpoint, error) {
	endpoints := []core.Endpoint{
		{
			ID:         "ep_ok",
			URL:        "http://dummy-receiver:8080/ok",
			EventTypes: []string{"test.ok"},
			Status:     core.EndpointStatusActive,
		},
		{
			ID:         "ep_flaky",
			URL:        "http://dummy-receiver:8080/flaky?fail=2&status=503",
			EventTypes: []string{"test.retry"},
			Status:     core.EndpointStatusActive,
		},
		{
			ID:         "ep_disabled",
			URL:        "http://dummy-receiver:8080/disabled",
			EventTypes: []string{"test.ok"},
			Status:     core.EndpointStatusDisabled,
		},
	}
	for i := range endpoints {
		e := &endpoints[i]
		e.TenantID = seedTenantID
		secret, err := c.Encrypt(ctx, core.SecretContext{TenantID: e.TenantID, EndpointID: e.ID}, []byte(signingSecret))
		if err != nil {
			return nil, fmt.Errorf("encrypt secret of %s: %w", e.ID, err)
		}
		e.EncryptedSecret = secret
	}
	return endpoints, nil
}
