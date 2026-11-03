package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"
)

// errInvalidSigningSecrets は SIGNING_SECRETS の値が使えないことを表す。
// エラーにはシークレットの値を含めない。何番目が不正かだけを示す。
var errInvalidSigningSecrets = errors.New("invalid SIGNING_SECRETS")

// parseSigningSecrets はカンマ区切りの whsec_ シークレットを検証器に変換する。
// 公式ライブラリは接頭辞や鍵の長さを検査しないので、ここで core と同じ条件を確かめる。
// 空は受け付けない。署名を検証しない受信は、配信の成功を誤って示すため。
func parseSigningSecrets(value string) ([]*standardwebhooks.Webhook, error) {
	if value == "" {
		return nil, fmt.Errorf("%w: empty", errInvalidSigningSecrets)
	}

	secrets := strings.Split(value, ",")
	webhooks := make([]*standardwebhooks.Webhook, 0, len(secrets))
	for i, secret := range secrets {
		if err := validateSigningSecret(secret); err != nil {
			return nil, fmt.Errorf("%w: entry %d: %w", errInvalidSigningSecrets, i+1, err)
		}
		wh, err := standardwebhooks.NewWebhook(secret)
		if err != nil {
			// validateSigningSecret で復号できることを確かめているので、ここには来ない想定
			return nil, fmt.Errorf("%w: entry %d", errInvalidSigningSecrets, i+1)
		}
		webhooks = append(webhooks, wh)
	}

	return webhooks, nil
}

func validateSigningSecret(secret string) error {
	encoded, found := strings.CutPrefix(secret, "whsec_")
	if !found {
		return errors.New("missing whsec_ prefix")
	}

	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return errors.New("not standard base64")
	}

	if len(key) < 24 || len(key) > 64 {
		return errors.New("key must be 24 to 64 bytes")
	}

	return nil
}
