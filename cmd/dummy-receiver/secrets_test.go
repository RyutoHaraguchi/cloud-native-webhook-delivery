package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// testSecret は n バイトの鍵を持つ whsec_ シークレットを返す。fill で鍵を区別する。
func testSecret(n int, fill byte) string {
	return "whsec_" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, n))
}

func TestParseSigningSecrets(t *testing.T) {
	valid := testSecret(32, 1)

	tests := []struct {
		name    string
		value   string
		wantLen int
		wantErr bool
	}{
		{name: "1 つ", value: valid, wantLen: 1},
		{name: "複数", value: valid + "," + testSecret(32, 2), wantLen: 2},
		{name: "鍵の長さの下限", value: testSecret(24, 1), wantLen: 1},
		{name: "鍵の長さの上限", value: testSecret(64, 1), wantLen: 1},
		{name: "空", value: "", wantErr: true},
		{name: "空の要素", value: valid + ",," + valid, wantErr: true},
		{name: "末尾のカンマ", value: valid + ",", wantErr: true},
		{name: "接頭辞なし", value: strings.TrimPrefix(valid, "whsec_"), wantErr: true},
		{name: "base64 でない", value: "whsec_!!!", wantErr: true},
		{name: "鍵が短い", value: testSecret(23, 1), wantErr: true},
		{name: "鍵が長い", value: testSecret(65, 1), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSigningSecrets(tt.value)
			if tt.wantErr {
				if !errors.Is(err, errInvalidSigningSecrets) {
					t.Fatalf("err = %v, want errInvalidSigningSecrets", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if len(got) != tt.wantLen {
				t.Errorf("len = %d, want %d", len(got), tt.wantLen)
			}
		})
	}
}

func TestParseSigningSecretsErrorOmitsSecret(t *testing.T) {
	secret := "whsec_c2hvcnQ=" // 鍵が短い
	_, err := parseSigningSecrets(secret)
	if err == nil {
		t.Fatal("err = nil")
	}
	if strings.Contains(err.Error(), "c2hvcnQ") {
		t.Errorf("エラーにシークレットが含まれる: %v", err)
	}
}
