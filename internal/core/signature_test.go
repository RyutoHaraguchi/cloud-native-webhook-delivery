package core

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// Standard Webhooks の公式ライブラリのテストにある入力と期待値
const (
	officialSecret    = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	officialMessageID = "msg_p5jXN8AQM9LWM0D4loKWxJek"
	officialSignature = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="
)

var (
	officialTimestamp = time.Unix(1614265330, 0)
	officialBody      = []byte(`{"test": 2432232314}`)
)

// 32 バイトの鍵 0123456789abcdef0123456789abcdef と、公式と同じ入力から openssl で計算した期待値
const (
	opensslSecret    = "whsec_MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	opensslSignature = "v1,hK4CWvngkaAwjY1pABLuUbOIkEJjW9tAapq1no4ojG0="
)

func secretOfLength(length int) string {
	return "whsec_" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("k"), length))
}

func TestSignatureHeader(t *testing.T) {
	tests := []struct {
		name    string
		secrets []string
		want    string
	}{
		{"公式のテストベクタ", []string{officialSecret}, officialSignature},
		{"32 バイトの鍵", []string{opensslSecret}, opensslSignature},
		{"標準の Base64 にだけある + と / を含む鍵", []string{"whsec_+/v7+/v7+/v7+/v7+/v7+/v7+/v7+/v7+/v7+/v7+/s="}, "v1,R5Yso1+kyRe5AvGOQMDsmjtvB0xsg626TrDDqG25BSs="},
		{"上限の 64 バイトの鍵", []string{secretOfLength(64)}, "v1,k9Iz2ky5aLTNr3elzdxy7EmRtpF8xeSnXA5qkTBYre8="},
		{"新旧 2 つの署名を渡した順に併記する", []string{opensslSecret, officialSecret}, opensslSignature + " " + officialSignature},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := SignatureHeader(test.secrets, officialMessageID, officialTimestamp, officialBody)
			if err != nil {
				t.Fatalf("SignatureHeader() error = %v", err)
			}
			if got != test.want {
				t.Errorf("SignatureHeader() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSignatureHeaderSignsBodyAsIs(t *testing.T) {
	// 前後の空白と改行も署名の対象になる。期待値は openssl で計算した
	body := []byte(" {\"test\": 2432232314}\n")
	const want = "v1,jW4n4XfNn78zIVe8bXfVHWXR5Hbnh67Z+eBDm4IYGFE="

	got, err := SignatureHeader([]string{opensslSecret}, officialMessageID, officialTimestamp, body)
	if err != nil {
		t.Fatalf("SignatureHeader() error = %v", err)
	}
	if got != want {
		t.Errorf("SignatureHeader() = %q, want %q", got, want)
	}
}

func TestSignatureHeaderTimestamp(t *testing.T) {
	// 署名に使うのは秒単位の Unix 時刻なので、タイムゾーンと秒未満は結果に影響しない
	timestamp := officialTimestamp.In(time.FixedZone("JST", 9*60*60)).Add(999 * time.Millisecond)

	got, err := SignatureHeader([]string{officialSecret}, officialMessageID, timestamp, officialBody)
	if err != nil {
		t.Fatalf("SignatureHeader() error = %v", err)
	}
	if got != officialSignature {
		t.Errorf("SignatureHeader() = %q, want %q", got, officialSignature)
	}
}

func TestSignatureHeaderSecretLength(t *testing.T) {
	tests := []struct {
		name    string
		length  int
		wantErr error
	}{
		{"下限の直前", 23, ErrInvalidSigningSecret},
		{"下限", 24, nil},
		{"発行する長さ", 32, nil},
		{"上限", 64, nil},
		{"上限の直後", 65, ErrInvalidSigningSecret},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := SignatureHeader([]string{secretOfLength(test.length)}, officialMessageID, officialTimestamp, officialBody)
			if !errors.Is(err, test.wantErr) {
				t.Errorf("SignatureHeader() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestSignatureHeaderInvalidSecret(t *testing.T) {
	tests := []struct {
		name    string
		secrets []string
	}{
		{"一覧が nil", nil},
		{"一覧が空", []string{}},
		{"空文字", []string{""}},
		{"接頭辞だけ", []string{"whsec_"}},
		{"接頭辞がない", []string{strings.TrimPrefix(officialSecret, "whsec_")}},
		{"別の接頭辞", []string{"whsk_" + strings.TrimPrefix(officialSecret, "whsec_")}},
		{"Base64 として読めない", []string{"whsec_not!base64!not!base64!not!base64"}},
		{"鍵が短い", []string{secretOfLength(23)}},
		{"2 つ目が不正", []string{officialSecret, "invalid"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := SignatureHeader(test.secrets, officialMessageID, officialTimestamp, officialBody)
			// シークレットがログに残らないよう、エラー文は固定の文言だけにする
			if !errors.Is(err, ErrInvalidSigningSecret) || err.Error() != ErrInvalidSigningSecret.Error() {
				t.Errorf("SignatureHeader() error = %v, want %v", err, ErrInvalidSigningSecret)
			}
			if got != "" {
				t.Errorf("SignatureHeader() = %q, want empty", got)
			}
		})
	}
}

func TestSignatureHeaderInvalidMessageID(t *testing.T) {
	tests := []struct {
		name      string
		messageID string
	}{
		{"空文字", ""},
		{"区切り文字を含む", "msg_1.100"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := SignatureHeader([]string{officialSecret}, test.messageID, officialTimestamp, officialBody)
			if !errors.Is(err, ErrInvalidMessageID) {
				t.Errorf("SignatureHeader() error = %v, want %v", err, ErrInvalidMessageID)
			}
			if got != "" {
				t.Errorf("SignatureHeader() = %q, want empty", got)
			}
		})
	}
}
