package main

import (
	"bytes"
	"encoding/base64"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// テスト用の固定値。ローカル専用で、どこでも使わない。
var (
	testKeyBytes = bytes.Repeat([]byte{0x02}, 32)
	testKey      = base64.StdEncoding.EncodeToString(testKeyBytes)
)

func validEnv() map[string]string {
	return map[string]string{
		"MESSAGES_TABLE":     "messages",
		"CONFIG_TABLE":       "config",
		"DELIVERY_QUEUE_URL": "http://elasticmq:9324/000000000000/deliveries",
		"DELIVERY_DLQ_URL":   "http://elasticmq:9324/000000000000/deliveries-dlq",
		"MESSAGE_RETENTION":  "168h",
		"LOCAL_SECRET_KEY":   testKey,
		"SSRF_ALLOWED_HOSTS": "dummy-receiver, other",
		"RETRY_SCHEDULE":     "1s,2s",
	}
}

func load(env map[string]string) (config, error) {
	return loadConfig(func(k string) string { return env[k] })
}

func TestLoadConfig_Valid(t *testing.T) {
	cfg, err := load(validEnv())
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.MessagesTable != "messages" {
		t.Errorf("MessagesTable = %q", cfg.MessagesTable)
	}
	if cfg.ConfigTable != "config" {
		t.Errorf("ConfigTable = %q", cfg.ConfigTable)
	}
	if cfg.QueueURL != "http://elasticmq:9324/000000000000/deliveries" {
		t.Errorf("QueueURL = %q", cfg.QueueURL)
	}
	if cfg.DLQURL != "http://elasticmq:9324/000000000000/deliveries-dlq" {
		t.Errorf("DLQURL = %q", cfg.DLQURL)
	}
	if cfg.Retention != 168*time.Hour {
		t.Errorf("Retention = %s", cfg.Retention)
	}
	if !bytes.Equal(cfg.Key, testKeyBytes) {
		t.Error("Key does not match the decoded LOCAL_SECRET_KEY")
	}
	if want := []string{"dummy-receiver", "other"}; !slices.Equal(cfg.AllowedHosts, want) {
		t.Errorf("AllowedHosts = %q, want %q", cfg.AllowedHosts, want)
	}
	if want := (core.RetrySchedule{time.Second, 2 * time.Second}); !slices.Equal(cfg.Schedule, want) {
		t.Errorf("Schedule = %v, want %v", cfg.Schedule, want)
	}
}

func TestLoadConfig_AllowedHostsDropsEmpty(t *testing.T) {
	env := validEnv()
	env["SSRF_ALLOWED_HOSTS"] = " a ,, b, "
	cfg, err := load(env)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if want := []string{"a", "b"}; !slices.Equal(cfg.AllowedHosts, want) {
		t.Errorf("AllowedHosts = %q, want %q", cfg.AllowedHosts, want)
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	env := validEnv()
	delete(env, "SSRF_ALLOWED_HOSTS")
	delete(env, "RETRY_SCHEDULE")
	cfg, err := load(env)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if len(cfg.AllowedHosts) != 0 {
		t.Errorf("AllowedHosts = %q, want empty", cfg.AllowedHosts)
	}
	if !slices.Equal(cfg.Schedule, core.DefaultRetrySchedule) {
		t.Errorf("Schedule = %v, want %v", cfg.Schedule, core.DefaultRetrySchedule)
	}
}

func TestLoadConfig_Missing(t *testing.T) {
	for _, name := range []string{
		"MESSAGES_TABLE",
		"CONFIG_TABLE",
		"DELIVERY_QUEUE_URL",
		"DELIVERY_DLQ_URL",
		"MESSAGE_RETENTION",
		"LOCAL_SECRET_KEY",
	} {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
			env[name] = ""
			_, err := load(env)
			if err == nil {
				t.Fatal("loadConfig() error = nil, want error")
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("loadConfig() error %q does not name %s", err, name)
			}
		})
	}
}

func TestLoadConfig_Invalid(t *testing.T) {
	shortKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x02}, 31))
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "retention not a duration", key: "MESSAGE_RETENTION", value: "abc"},
		{name: "schedule with zero", key: "RETRY_SCHEDULE", value: "1s,0s"},
		{name: "schedule with empty element", key: "RETRY_SCHEDULE", value: "1s,,2s"},
		{name: "schedule negative", key: "RETRY_SCHEDULE", value: "-1s"},
		{name: "key of 31 bytes", key: "LOCAL_SECRET_KEY", value: shortKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			env[tt.key] = tt.value
			_, err := load(env)
			if err == nil {
				t.Fatal("loadConfig() error = nil, want error")
			}
			// エラーに鍵の値を含めない
			if strings.Contains(err.Error(), env["LOCAL_SECRET_KEY"]) {
				t.Errorf("loadConfig() error %q contains LOCAL_SECRET_KEY", err)
			}
		})
	}
}
