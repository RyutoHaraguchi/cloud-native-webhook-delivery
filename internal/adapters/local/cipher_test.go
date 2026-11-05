package local

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"testing"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// テスト用の固定鍵。ローカル専用で、どこでも使わない。
var (
	testKey      = bytes.Repeat([]byte{0x01}, 32)
	otherTestKey = bytes.Repeat([]byte{0x02}, 32)
	testContext  = core.SecretContext{TenantID: "t_a", EndpointID: "ep_1"}
	testSecret   = []byte("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw")
)

func newTestCipher(t *testing.T, key []byte) *Cipher {
	t.Helper()
	c, err := NewCipher(key, rand.Reader)
	if err != nil {
		t.Fatalf("NewCipher() = %v", err)
	}
	return c
}

func encrypt(t *testing.T, c *Cipher, sc core.SecretContext, plaintext []byte) []byte {
	t.Helper()
	ciphertext, err := c.Encrypt(context.Background(), sc, plaintext)
	if err != nil {
		t.Fatalf("Encrypt() = %v", err)
	}
	return ciphertext
}

// fatalReader は読まれたらテストを失敗させる Reader。
type fatalReader struct{ t *testing.T }

func (r fatalReader) Read([]byte) (int, error) {
	r.t.Fatal("random が読まれた")
	return 0, nil
}

// errReader は常にエラーを返す Reader。
type errReader struct{}

var errRandom = errors.New("random failure")

func (errReader) Read([]byte) (int, error) { return 0, errRandom }

func TestParseKey(t *testing.T) {
	key32 := bytes.Repeat([]byte{0xab}, 32)
	tests := []struct {
		name    string
		input   string
		want    []byte
		wantErr bool
	}{
		{"32 バイト", base64.StdEncoding.EncodeToString(key32), key32, false},
		{"16 バイト", base64.StdEncoding.EncodeToString(make([]byte, 16)), nil, true},
		{"33 バイト", base64.StdEncoding.EncodeToString(make([]byte, 33)), nil, true},
		{"空", "", nil, true},
		{"base64 が不正", "!!!not-base64!!!", nil, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseKey(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ParseKey(%q) = nil error, want error", test.input)
				}
				if test.input != "" && bytes.Contains([]byte(err.Error()), []byte(test.input)) {
					t.Errorf("エラー %q に入力値が含まれる", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseKey() = %v", err)
			}
			if !bytes.Equal(got, test.want) {
				t.Errorf("ParseKey() = %x, want %x", got, test.want)
			}
		})
	}
}

func TestNewCipher(t *testing.T) {
	tests := []struct {
		name   string
		key    []byte
		random io.Reader
	}{
		{"31 バイトの鍵", make([]byte, 31), rand.Reader},
		{"nil の random", testKey, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewCipher(test.key, test.random); err == nil {
				t.Fatal("NewCipher() = nil error, want error")
			}
		})
	}
}

func TestCipher_RoundTrip(t *testing.T) {
	c := newTestCipher(t, testKey)
	ciphertext := encrypt(t, c, testContext, testSecret)
	if bytes.Contains(ciphertext, testSecret) {
		t.Error("暗号文に平文が含まれる")
	}
	got, err := c.Decrypt(context.Background(), testContext, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt() = %v", err)
	}
	if !bytes.Equal(got, testSecret) {
		t.Errorf("Decrypt() = %q, want %q", got, testSecret)
	}
}

func TestCipher_Layout(t *testing.T) {
	nonce := []byte("0123456789ab")
	c, err := NewCipher(testKey, bytes.NewReader(nonce))
	if err != nil {
		t.Fatalf("NewCipher() = %v", err)
	}
	ciphertext := encrypt(t, c, testContext, testSecret)
	if !bytes.Equal(ciphertext[:12], nonce) {
		t.Errorf("先頭 12 バイト = %x, want %x", ciphertext[:12], nonce)
	}
	if want := 12 + len(testSecret) + 16; len(ciphertext) != want {
		t.Errorf("len(ciphertext) = %d, want %d", len(ciphertext), want)
	}
}

func TestCipher_NonceDiffers(t *testing.T) {
	c := newTestCipher(t, testKey)
	a := encrypt(t, c, testContext, testSecret)
	b := encrypt(t, c, testContext, testSecret)
	if bytes.Equal(a, b) {
		t.Error("同じ平文の暗号文が一致した")
	}
}

func TestCipher_ContextMismatch(t *testing.T) {
	c := newTestCipher(t, testKey)
	ciphertext := encrypt(t, c, testContext, testSecret)
	for _, sc := range []core.SecretContext{
		{TenantID: "t_b", EndpointID: "ep_1"},
		{TenantID: "t_a", EndpointID: "ep_2"},
	} {
		_, err := c.Decrypt(context.Background(), sc, ciphertext)
		if !errors.Is(err, core.ErrUndecryptable) {
			t.Errorf("Decrypt(%+v) = %v, want ErrUndecryptable", sc, err)
		}
	}
}

func TestCipher_Tampered(t *testing.T) {
	c := newTestCipher(t, testKey)
	ciphertext := encrypt(t, c, testContext, testSecret)
	ciphertext[len(ciphertext)-1] ^= 0xff
	_, err := c.Decrypt(context.Background(), testContext, ciphertext)
	if !errors.Is(err, core.ErrUndecryptable) {
		t.Errorf("Decrypt() = %v, want ErrUndecryptable", err)
	}
}

func TestCipher_WrongKey(t *testing.T) {
	ciphertext := encrypt(t, newTestCipher(t, testKey), testContext, testSecret)
	_, err := newTestCipher(t, otherTestKey).Decrypt(context.Background(), testContext, ciphertext)
	if !errors.Is(err, core.ErrUndecryptable) {
		t.Errorf("Decrypt() = %v, want ErrUndecryptable", err)
	}
}

func TestCipher_ShortCiphertext(t *testing.T) {
	c := newTestCipher(t, testKey)
	tests := []struct {
		name       string
		ciphertext []byte
	}{
		{"nil", nil},
		{"11 バイト", make([]byte, 11)},
		{"12 バイト（タグなし）", make([]byte, 12)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := c.Decrypt(context.Background(), testContext, test.ciphertext)
			if !errors.Is(err, core.ErrUndecryptable) {
				t.Errorf("Decrypt() = %v, want ErrUndecryptable", err)
			}
		})
	}
}

func TestCipher_InvalidContext(t *testing.T) {
	c, err := NewCipher(testKey, fatalReader{t: t})
	if err != nil {
		t.Fatalf("NewCipher() = %v", err)
	}
	sc := core.SecretContext{TenantID: "", EndpointID: "ep_1"}
	if _, err := c.Encrypt(context.Background(), sc, testSecret); !errors.Is(err, core.ErrInvalidKey) {
		t.Errorf("Encrypt() = %v, want ErrInvalidKey", err)
	}
	ciphertext := encrypt(t, newTestCipher(t, testKey), testContext, testSecret)
	if _, err := c.Decrypt(context.Background(), sc, ciphertext); !errors.Is(err, core.ErrInvalidKey) {
		t.Errorf("Decrypt() = %v, want ErrInvalidKey", err)
	}
}

func TestCipher_RandomError(t *testing.T) {
	c, err := NewCipher(testKey, errReader{})
	if err != nil {
		t.Fatalf("NewCipher() = %v", err)
	}
	_, err = c.Encrypt(context.Background(), testContext, testSecret)
	if err == nil {
		t.Fatal("Encrypt() = nil error, want error")
	}
	if errors.Is(err, core.ErrUndecryptable) {
		t.Errorf("Encrypt() = %v, ErrUndecryptable であってはならない", err)
	}
}
