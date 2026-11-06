package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/adapters/local"
	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// config は delivery-worker の設定。値は環境変数から読む
type config struct {
	// MessagesTable は messages テーブルの名前
	MessagesTable string
	// ConfigTable は config テーブルの名前
	ConfigTable string
	// QueueURL は配信キューの URL
	QueueURL string
	// DLQURL は配信の DLQ の URL
	DLQURL string
	// Retention は messages の項目を TTL で消すまでの期間
	Retention time.Duration
	// Key はローカル用 SecretCipher の 32 バイトの鍵
	Key []byte
	// AllowedHosts は SSRF 検査の例外にするホスト名
	AllowedHosts []string
	// Schedule は再送の間隔
	Schedule core.RetrySchedule
}

// loadConfig は getenv から設定を読む。必須の値が欠けるか不正ならエラーを返す。
// エラーに鍵の値を含めない
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
		MessagesTable: require("MESSAGES_TABLE"),
		ConfigTable:   require("CONFIG_TABLE"),
		QueueURL:      require("DELIVERY_QUEUE_URL"),
		DLQURL:        require("DELIVERY_DLQ_URL"),
	}
	rawRetention := require("MESSAGE_RETENTION")
	rawKey := require("LOCAL_SECRET_KEY")
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}

	retention, err := time.ParseDuration(rawRetention)
	if err != nil {
		return config{}, fmt.Errorf("MESSAGE_RETENTION: %w", err)
	}
	if retention <= 0 {
		return config{}, fmt.Errorf("MESSAGE_RETENTION must be positive, got %s", retention)
	}
	cfg.Retention = retention

	key, err := local.ParseKey(rawKey)
	if err != nil {
		return config{}, fmt.Errorf("LOCAL_SECRET_KEY: %w", err)
	}
	cfg.Key = key

	cfg.AllowedHosts = parseAllowedHosts(getenv("SSRF_ALLOWED_HOSTS"))

	schedule, err := parseRetrySchedule(getenv("RETRY_SCHEDULE"))
	if err != nil {
		return config{}, fmt.Errorf("RETRY_SCHEDULE: %w", err)
	}
	cfg.Schedule = schedule
	return cfg, nil
}

// parseAllowedHosts はカンマ区切りのホスト名を分ける。前後の空白を除き、空の要素は捨てる
func parseAllowedHosts(raw string) []string {
	var hosts []string
	for h := range strings.SplitSeq(raw, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// parseRetrySchedule はカンマ区切りの time.ParseDuration 形式を読む。
// 空なら core.DefaultRetrySchedule を返す。空の要素と 0 以下の値はエラーにする
func parseRetrySchedule(raw string) (core.RetrySchedule, error) {
	if raw == "" {
		return core.DefaultRetrySchedule, nil
	}
	var schedule core.RetrySchedule
	for i, s := range strings.Split(raw, ",") {
		d, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", i, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("element %d must be positive, got %s", i, d)
		}
		schedule = append(schedule, d)
	}
	return schedule, nil
}
