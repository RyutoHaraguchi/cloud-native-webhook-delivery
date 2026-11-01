package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// エラーにはシークレットの値を含めない。ログに残っても漏れないようにするため。
var (
	ErrInvalidSigningSecret = errors.New("invalid signing secret")
	ErrInvalidMessageID     = errors.New("invalid message id")
)

// SignatureHeader は Standard Webhooks の webhook-signature ヘッダーの値を返す。
// secrets の順に署名を作り、スペースで区切って併記する。
// 署名の前提はここで確かめる。満たさないときはエラーを返すので、送信してはいけない。
func SignatureHeader(secrets []string, messageID string, timestamp time.Time, body []byte) (string, error) {
	// ID に区切り文字が入ると、別の ID、時刻、本文の組と同じ署名になる
	if messageID == "" || strings.Contains(messageID, ".") {
		return "", ErrInvalidMessageID
	}
	if len(secrets) == 0 {
		return "", ErrInvalidSigningSecret
	}

	signatures := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		key, err := decodeSigningSecret(secret)
		if err != nil {
			return "", err
		}
		signatures = append(signatures, sign(key, messageID, timestamp, body))
	}

	return strings.Join(signatures, " "), nil
}

// decodeSigningSecret は whsec_ で始まるシークレットを鍵に変換する。
func decodeSigningSecret(secret string) ([]byte, error) {
	after, found := strings.CutPrefix(secret, "whsec_")
	if !found {
		return nil, ErrInvalidSigningSecret
	}

	key, err := base64.StdEncoding.DecodeString(after)
	if err != nil {
		return nil, ErrInvalidSigningSecret
	}

	if len(key) < 24 || len(key) > 64 {
		return nil, ErrInvalidSigningSecret
	}

	return key, nil
}

// sign は 1 つの鍵で署名を作る。本文は送信するバイト列のまま署名する。
func sign(key []byte, messageID string, timestamp time.Time, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(fmt.Appendf(nil, "%s.%d.", messageID, timestamp.Unix()))
	mac.Write(body)

	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
