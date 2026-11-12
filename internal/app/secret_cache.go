package app

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

var _ core.SecretCipher = (*CachedCipher)(nil)

// CachedCipher は inner の復号結果を ttl のあいだ保持する core.SecretCipher。
// 同じエンドポイントへの配信が続くときに、復号（本番では KMS の呼び出し）を減らす。
// 同時に呼ばれてもよい。同じキーを同時に取りこぼしたときは inner を複数回呼ぶことがある。
type CachedCipher struct {
	inner core.SecretCipher
	ttl   time.Duration
	now   func() time.Time

	mu      sync.Mutex
	entries map[cacheKey]cacheEntry
}

// cacheKey は復号結果を引くキー。同じ暗号文でも、別のテナントやエンドポイントの文脈では別に扱う。
type cacheKey struct {
	tenantID   string
	endpointID string
	ciphertext string
}

type cacheEntry struct {
	plaintext []byte
	expiresAt time.Time
}

// NewCachedCipher は inner の復号結果を ttl のあいだ保持する CachedCipher を作る。now は現在時刻を返す。
func NewCachedCipher(inner core.SecretCipher, ttl time.Duration, now func() time.Time) *CachedCipher {
	return &CachedCipher{
		inner:   inner,
		ttl:     ttl,
		now:     now,
		entries: make(map[cacheKey]cacheEntry),
	}
}

// Encrypt は保持せずにそのまま inner に渡す。
func (c *CachedCipher) Encrypt(ctx context.Context, sc core.SecretContext, plaintext []byte) ([]byte, error) {
	// キャッシュは透過的な層なので、inner のエラーに文脈を足さずにそのまま返す
	return c.inner.Encrypt(ctx, sc, plaintext)
}

// Decrypt は期限内の保持があればその写しを返し、なければ inner で復号して保持する。
// エラーは保持しない。返す平文は呼び出し側が書き換えてもよい写し。
func (c *CachedCipher) Decrypt(ctx context.Context, sc core.SecretContext, ciphertext []byte) ([]byte, error) {
	key := cacheKey{tenantID: sc.TenantID, endpointID: sc.EndpointID, ciphertext: string(ciphertext)}

	if pt, ok := c.lookup(key); ok {
		return pt, nil
	}

	// inner の呼び出しは I/O を伴いうるので、ロックの外で行う
	pt, err := c.inner.Decrypt(ctx, sc, ciphertext)
	if err != nil {
		// キャッシュは透過的な層なので、inner のエラー（ErrUndecryptable など）をそのまま返す
		return nil, err
	}

	c.store(key, pt)
	return pt, nil
}

// lookup は期限内の保持があれば、その写しを返す。
func (c *CachedCipher) lookup(key cacheKey) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[key]
	if !ok || !c.now().Before(e.expiresAt) {
		return nil, false
	}
	return bytes.Clone(e.plaintext), true
}

// store は期限切れの保持をまとめて捨ててから、plaintext の写しを保持する。
func (c *CachedCipher) store(key cacheKey, plaintext []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	for k, e := range c.entries {
		if !now.Before(e.expiresAt) {
			delete(c.entries, k)
		}
	}
	c.entries[key] = cacheEntry{plaintext: bytes.Clone(plaintext), expiresAt: now.Add(c.ttl)}
}
