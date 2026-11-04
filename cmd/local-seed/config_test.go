package main

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// テスト用の固定値。ローカル専用で、どこでも使わない。
var (
	testKeyBytes      = bytes.Repeat([]byte{0x01}, 32)
	testKey           = base64.StdEncoding.EncodeToString(testKeyBytes)
	testSigningSecret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
)

func validEnv() map[string]string {
	return map[string]string{
		"CONFIG_TABLE":              "config",
		"AWS_ENDPOINT_URL_DYNAMODB": "http://dynamodb:8000",
		"LOCAL_SECRET_KEY":          testKey,
		"SEED_SIGNING_SECRET":       testSigningSecret,
	}
}

func TestLoadConfig(t *testing.T) {
	shortKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, 16))
	tests := []struct {
		name    string
		modify  func(env map[string]string)
		wantErr bool
		wantURL string
	}{
		{name: "all set", modify: func(map[string]string) {}, wantURL: "http://dynamodb:8000"},
		{name: "missing table", modify: func(env map[string]string) { delete(env, "CONFIG_TABLE") }, wantErr: true},
		{name: "missing endpoint url", modify: func(env map[string]string) { delete(env, "AWS_ENDPOINT_URL_DYNAMODB") }, wantErr: true},
		{name: "missing key", modify: func(env map[string]string) { delete(env, "LOCAL_SECRET_KEY") }, wantErr: true},
		{name: "missing signing secret", modify: func(env map[string]string) { delete(env, "SEED_SIGNING_SECRET") }, wantErr: true},
		{
			name:    "localhost",
			modify:  func(env map[string]string) { env["AWS_ENDPOINT_URL_DYNAMODB"] = "http://localhost:8000" },
			wantURL: "http://localhost:8000",
		},
		{
			name:    "loopback ip",
			modify:  func(env map[string]string) { env["AWS_ENDPOINT_URL_DYNAMODB"] = "http://127.0.0.1:8000" },
			wantURL: "http://127.0.0.1:8000",
		},
		{
			name: "real aws",
			modify: func(env map[string]string) {
				env["AWS_ENDPOINT_URL_DYNAMODB"] = "https://dynamodb.ap-northeast-1.amazonaws.com"
			},
			wantErr: true,
		},
		{
			name:    "subdomain of allowed name",
			modify:  func(env map[string]string) { env["AWS_ENDPOINT_URL_DYNAMODB"] = "http://dynamodb.example.com:8000" },
			wantErr: true,
		},
		{
			name:    "suffix of allowed name",
			modify:  func(env map[string]string) { env["AWS_ENDPOINT_URL_DYNAMODB"] = "http://evil-localhost:8000" },
			wantErr: true,
		},
		{name: "short key", modify: func(env map[string]string) { env["LOCAL_SECRET_KEY"] = shortKey }, wantErr: true},
		{
			name:    "signing secret without prefix",
			modify:  func(env map[string]string) { env["SEED_SIGNING_SECRET"] = "MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw" },
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			tt.modify(env)
			cfg, err := loadConfig(func(k string) string { return env[k] })
			if tt.wantErr {
				if err == nil {
					t.Fatal("loadConfig() error = nil, want error")
				}
				// エラーに鍵とシークレットの値を含めない
				for _, secret := range []string{env["LOCAL_SECRET_KEY"], env["SEED_SIGNING_SECRET"]} {
					if secret != "" && strings.Contains(err.Error(), secret) {
						t.Errorf("loadConfig() error %q contains a secret value", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfig() error = %v", err)
			}
			if cfg.Table != "config" {
				t.Errorf("Table = %q, want %q", cfg.Table, "config")
			}
			if cfg.DynamoDBURL != tt.wantURL {
				t.Errorf("DynamoDBURL = %q, want %q", cfg.DynamoDBURL, tt.wantURL)
			}
			if !bytes.Equal(cfg.Key, testKeyBytes) {
				t.Error("Key does not match the decoded LOCAL_SECRET_KEY")
			}
			if cfg.SigningSecret != testSigningSecret {
				t.Error("SigningSecret does not match SEED_SIGNING_SECRET")
			}
		})
	}
}
