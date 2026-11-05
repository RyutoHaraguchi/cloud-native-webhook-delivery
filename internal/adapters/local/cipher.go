package local

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

const (
	// keySize は AES-256 の鍵の長さ。
	keySize = 32
	// nonceSize は GCM の標準の nonce の長さ。
	nonceSize = 12
)

// ParseKey は標準 base64 の文字列を復号し、32 バイトの鍵として返す。
// エラーに入力値を含めない。
func ParseKey(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("local secret key is empty")
	}
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		// base64.CorruptInputError は不正な位置の整数だけを持ち、鍵の値を含まないので包んでよい
		return nil, fmt.Errorf("local secret key is not valid base64: %w", err)
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("local secret key must be %d bytes, got %d", keySize, len(key))
	}
	return key, nil
}

// Cipher は KMS の代わりに AES-256-GCM で暗号化する、ローカル用の SecretCipher。
// 暗号文は nonce（12 バイト）と GCM の出力を連結したもので、
// AAD に TENANT#<tenantId>#ENDPOINT#<endpointId> を入れて暗号化コンテキストと同じ効果を得る。
type Cipher struct {
	aead   cipher.AEAD
	random io.Reader
}

var _ core.SecretCipher = (*Cipher)(nil)

// NewCipher は 32 バイトの鍵と nonce を作る乱数源から Cipher を作る。
// 本番のコードでは random に crypto/rand.Reader を渡す。
func NewCipher(key []byte, random io.Reader) (*Cipher, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("local secret key must be %d bytes, got %d", keySize, len(key))
	}
	if random == nil {
		return nil, errors.New("random reader is nil")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	return &Cipher{aead: aead, random: random}, nil
}

// Encrypt は plaintext を sc に結び付けて暗号化する。sc が不正なら ErrInvalidKey を返す。
func (c *Cipher) Encrypt(_ context.Context, sc core.SecretContext, plaintext []byte) ([]byte, error) {
	if err := sc.Validate(); err != nil {
		return nil, fmt.Errorf("encrypt secret: %w", err)
	}
	out := make([]byte, nonceSize, nonceSize+len(plaintext)+c.aead.Overhead())
	if _, err := io.ReadFull(c.random, out); err != nil {
		return nil, fmt.Errorf("encrypt secret: read nonce: %w", err)
	}
	return c.aead.Seal(out, out, plaintext, additionalData(sc)), nil
}

// Decrypt は ciphertext を復号する。暗号化と同じ sc でなければ ErrUndecryptable、
// sc が不正なら ErrInvalidKey を返す。
func (c *Cipher) Decrypt(_ context.Context, sc core.SecretContext, ciphertext []byte) ([]byte, error) {
	if err := sc.Validate(); err != nil {
		return nil, fmt.Errorf("decrypt secret: %w", err)
	}
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("decrypt secret: ciphertext too short: %w", core.ErrUndecryptable)
	}
	nonce, sealed := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := c.aead.Open(nil, nonce, sealed, additionalData(sc))
	if err != nil {
		// Open の失敗は改ざん、コンテキストの不一致、鍵の違いを区別しないので、元のエラーは捨てる
		return nil, fmt.Errorf("decrypt secret: %w", core.ErrUndecryptable)
	}
	return plaintext, nil
}

// additionalData は KMS の暗号化コンテキストに相当する AAD を作る。
func additionalData(sc core.SecretContext) []byte {
	return []byte("TENANT#" + sc.TenantID + "#ENDPOINT#" + sc.EndpointID)
}
