package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/adapters/local"
)

// allowedDynamoDBHosts はシードが書き込んでよい DynamoDB のホスト名。
// AWS の認証情報があるシェルで実行しても、実環境に書き込まないようにする
var allowedDynamoDBHosts = []string{"localhost", "127.0.0.1", "dynamodb"}

// signingSecretPrefix は Standard Webhooks の署名シークレットの接頭辞
const signingSecretPrefix = "whsec_"

// config は local-seed の設定。値はすべて環境変数から読み、既定値はない
type config struct {
	// Table は config テーブルの名前
	Table string
	// DynamoDBURL は DynamoDB Local の接続先
	DynamoDBURL string
	// Key はローカル用 SecretCipher の 32 バイトの鍵
	Key []byte
	// SigningSecret はエンドポイントに登録する署名シークレットの平文
	SigningSecret string
}

// loadConfig は getenv から設定を読む。1 つでも欠けるか不正ならエラーを返す。
// エラーに鍵とシークレットの値を含めない
func loadConfig(getenv func(string) string) (config, error) {
	var missing []string
	require := func(name string) string {
		v := getenv(name)
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}
	cfg := config{
		Table:         require("CONFIG_TABLE"),
		DynamoDBURL:   require("AWS_ENDPOINT_URL_DYNAMODB"),
		SigningSecret: require("SEED_SIGNING_SECRET"),
	}
	rawKey := require("LOCAL_SECRET_KEY")
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}

	if err := validateDynamoDBURL(cfg.DynamoDBURL); err != nil {
		return config{}, err
	}
	key, err := local.ParseKey(rawKey)
	if err != nil {
		return config{}, fmt.Errorf("LOCAL_SECRET_KEY: %w", err)
	}
	cfg.Key = key
	if !strings.HasPrefix(cfg.SigningSecret, signingSecretPrefix) {
		return config{}, fmt.Errorf("SEED_SIGNING_SECRET must start with %s", signingSecretPrefix)
	}
	return cfg, nil
}

// validateDynamoDBURL は接続先のホスト名がローカルのものかを確かめる。
// URL のユーザー情報にシークレットが入りうるので、エラーに入力値を含めない
func validateDynamoDBURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		// url.Error は入力の URL 全体を含むので、元のエラーは捨てる
		return errors.New("AWS_ENDPOINT_URL_DYNAMODB is not a valid URL")
	}
	for _, host := range allowedDynamoDBHosts {
		if u.Hostname() == host {
			return nil
		}
	}
	return errors.New("AWS_ENDPOINT_URL_DYNAMODB must point to localhost, 127.0.0.1 or dynamodb")
}
