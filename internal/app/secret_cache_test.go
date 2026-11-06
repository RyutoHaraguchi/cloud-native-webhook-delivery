package app

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// countingCipher は呼ばれた回数を数える偽の SecretCipher。
// decryptErrs に積んだエラーを先頭から順に返し、尽きたら暗号文の前に "plain:" を付けて返す。
type countingCipher struct {
	mu          sync.Mutex
	decrypts    int
	encrypts    int
	decryptErrs []error
}

func (c *countingCipher) Encrypt(_ context.Context, _ core.SecretContext, plaintext []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.encrypts++
	return append([]byte("enc:"), plaintext...), nil
}

func (c *countingCipher) Decrypt(_ context.Context, _ core.SecretContext, ciphertext []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.decrypts++
	if len(c.decryptErrs) > 0 {
		err := c.decryptErrs[0]
		c.decryptErrs = c.decryptErrs[1:]
		return nil, err
	}
	return append([]byte("plain:"), ciphertext...), nil
}

func (c *countingCipher) decryptCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.decrypts
}

// fakeClock は進められる時計。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var (
	scA  = core.SecretContext{TenantID: "t_a", EndpointID: "ep_1"}
	scA2 = core.SecretContext{TenantID: "t_a", EndpointID: "ep_2"}
)

func mustDecrypt(t *testing.T, c *CachedCipher, sc core.SecretContext, ct string) []byte {
	t.Helper()
	got, err := c.Decrypt(context.Background(), sc, []byte(ct))
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	return got
}

func TestCachedCipher_HitWithinTTL(t *testing.T) {
	inner := &countingCipher{}
	clock := newFakeClock()
	c := NewCachedCipher(inner, 5*time.Minute, clock.Now)

	first := mustDecrypt(t, c, scA, "ct1")
	clock.Advance(4 * time.Minute)
	second := mustDecrypt(t, c, scA, "ct1")

	if got := inner.decryptCount(); got != 1 {
		t.Errorf("inner Decrypt の回数 = %d, want 1", got)
	}
	if !bytes.Equal(first, second) || string(second) != "plain:ct1" {
		t.Errorf("結果 = %q, %q, want 両方 %q", first, second, "plain:ct1")
	}
}

func TestCachedCipher_ExpiresAfterTTL(t *testing.T) {
	inner := &countingCipher{}
	clock := newFakeClock()
	c := NewCachedCipher(inner, 5*time.Minute, clock.Now)

	mustDecrypt(t, c, scA, "ct1")
	clock.Advance(5*time.Minute + time.Second)
	if got := string(mustDecrypt(t, c, scA, "ct1")); got != "plain:ct1" {
		t.Errorf("結果 = %q, want %q", got, "plain:ct1")
	}

	if got := inner.decryptCount(); got != 2 {
		t.Errorf("inner Decrypt の回数 = %d, want 2", got)
	}
}

func TestCachedCipher_KeyIncludesEndpoint(t *testing.T) {
	inner := &countingCipher{}
	clock := newFakeClock()
	c := NewCachedCipher(inner, 5*time.Minute, clock.Now)

	mustDecrypt(t, c, scA, "ct1")
	// 別のエンドポイントの文脈では復号できない。キャッシュがあっても inner の判断がそのまま返る
	inner.decryptErrs = []error{core.ErrUndecryptable}
	_, err := c.Decrypt(context.Background(), scA2, []byte("ct1"))

	if !errors.Is(err, core.ErrUndecryptable) {
		t.Errorf("err = %v, want ErrUndecryptable", err)
	}
	if got := inner.decryptCount(); got != 2 {
		t.Errorf("inner Decrypt の回数 = %d, want 2", got)
	}
}

func TestCachedCipher_KeyIncludesTenant(t *testing.T) {
	inner := &countingCipher{}
	clock := newFakeClock()
	c := NewCachedCipher(inner, 5*time.Minute, clock.Now)

	mustDecrypt(t, c, scA, "ct1")
	mustDecrypt(t, c, core.SecretContext{TenantID: "t_b", EndpointID: "ep_1"}, "ct1")

	if got := inner.decryptCount(); got != 2 {
		t.Errorf("inner Decrypt の回数 = %d, want 2", got)
	}
}

func TestCachedCipher_KeyIncludesCiphertext(t *testing.T) {
	inner := &countingCipher{}
	clock := newFakeClock()
	c := NewCachedCipher(inner, 5*time.Minute, clock.Now)

	first := mustDecrypt(t, c, scA, "ct1")
	second := mustDecrypt(t, c, scA, "ct2")

	if got := inner.decryptCount(); got != 2 {
		t.Errorf("inner Decrypt の回数 = %d, want 2", got)
	}
	if string(first) != "plain:ct1" || string(second) != "plain:ct2" {
		t.Errorf("結果 = %q, %q", first, second)
	}
}

func TestCachedCipher_DoesNotCacheErrors(t *testing.T) {
	transient := errors.New("kms unavailable")
	inner := &countingCipher{decryptErrs: []error{transient}}
	clock := newFakeClock()
	c := NewCachedCipher(inner, 5*time.Minute, clock.Now)

	if _, err := c.Decrypt(context.Background(), scA, []byte("ct1")); !errors.Is(err, transient) {
		t.Fatalf("1 回目の err = %v, want %v", err, transient)
	}
	if got := string(mustDecrypt(t, c, scA, "ct1")); got != "plain:ct1" {
		t.Errorf("2 回目の結果 = %q, want %q", got, "plain:ct1")
	}

	if got := inner.decryptCount(); got != 2 {
		t.Errorf("inner Decrypt の回数 = %d, want 2", got)
	}
}

func TestCachedCipher_ReturnsCopy(t *testing.T) {
	inner := &countingCipher{}
	clock := newFakeClock()
	c := NewCachedCipher(inner, 5*time.Minute, clock.Now)

	first := mustDecrypt(t, c, scA, "ct1")
	for i := range first {
		first[i] = 'x'
	}
	second := mustDecrypt(t, c, scA, "ct1")
	for i := range second {
		second[i] = 'y'
	}
	third := mustDecrypt(t, c, scA, "ct1")

	if string(third) != "plain:ct1" {
		t.Errorf("書き換え後の結果 = %q, want %q", third, "plain:ct1")
	}
	if got := inner.decryptCount(); got != 1 {
		t.Errorf("inner Decrypt の回数 = %d, want 1", got)
	}
}

func TestCachedCipher_EvictsExpired(t *testing.T) {
	inner := &countingCipher{}
	clock := newFakeClock()
	c := NewCachedCipher(inner, 5*time.Minute, clock.Now)

	mustDecrypt(t, c, scA, "ct1")
	mustDecrypt(t, c, scA, "ct2")
	clock.Advance(3 * time.Minute)
	mustDecrypt(t, c, scA, "ct3")
	if got := len(c.entries); got != 3 {
		t.Fatalf("保持の数 = %d, want 3", got)
	}

	// ct1 と ct2 だけが期限切れになる
	clock.Advance(2*time.Minute + time.Second)
	mustDecrypt(t, c, scA, "ct4")

	if got := len(c.entries); got != 2 {
		t.Errorf("保持の数 = %d, want 2", got)
	}
	for _, ct := range []string{"ct1", "ct2"} {
		if _, ok := c.entries[cacheKey{tenantID: scA.TenantID, endpointID: scA.EndpointID, ciphertext: ct}]; ok {
			t.Errorf("期限切れの %s が残っている", ct)
		}
	}
}

func TestCachedCipher_EncryptPassesThrough(t *testing.T) {
	inner := &countingCipher{}
	clock := newFakeClock()
	c := NewCachedCipher(inner, 5*time.Minute, clock.Now)

	for range 2 {
		got, err := c.Encrypt(context.Background(), scA, []byte("secret"))
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		if string(got) != "enc:secret" {
			t.Errorf("結果 = %q, want %q", got, "enc:secret")
		}
	}

	if inner.encrypts != 2 {
		t.Errorf("inner Encrypt の回数 = %d, want 2", inner.encrypts)
	}
}

func TestCachedCipher_Concurrent(t *testing.T) {
	inner := &countingCipher{}
	clock := newFakeClock()
	c := NewCachedCipher(inner, 5*time.Minute, clock.Now)

	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := range 50 {
		wg.Go(func() {
			ct := []byte{'c', 't', byte('0' + i%5)}
			got, err := c.Decrypt(context.Background(), scA, ct)
			if err != nil {
				errs <- err
				return
			}
			if want := "plain:" + string(ct); string(got) != want {
				errs <- errors.New("結果が違う: " + string(got))
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
