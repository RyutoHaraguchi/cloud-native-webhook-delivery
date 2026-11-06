package core

import (
	"errors"
	"strings"
	"testing"
)

func validEndpoint() Endpoint {
	return Endpoint{
		TenantID:         "t_1",
		ID:               "ep_1",
		URL:              "https://example.com/webhook",
		EventTypes:       []string{"order.created"},
		EncryptedSecret:  []byte("secret"),
		EncryptedHeaders: []byte("headers"),
		Status:           EndpointStatusActive,
	}
}

func TestEndpointValidate(t *testing.T) {
	tests := []struct {
		name       string
		modify     func(e *Endpoint)
		wantErr    bool
		wantKeyErr bool
	}{
		{"有効", func(e *Endpoint) {}, false, false},
		{"無効化済み", func(e *Endpoint) { e.Status = EndpointStatusDisabled }, false, false},
		{"購読が空", func(e *Endpoint) { e.EventTypes = []string{} }, false, false},
		{"固定ヘッダーが nil", func(e *Endpoint) { e.EncryptedHeaders = nil }, false, false},
		{"固定ヘッダーが空", func(e *Endpoint) { e.EncryptedHeaders = []byte{} }, false, false},
		// scheme の制限は SSRF 検査と登録処理が担当する
		{"http の URL", func(e *Endpoint) { e.URL = "http://dummy-receiver:8080/a" }, false, false},
		{"状態の綴りが違う", func(e *Endpoint) { e.Status = "disabeld" }, true, false},
		{"状態が空", func(e *Endpoint) { e.Status = "" }, true, false},
		{"URL が空", func(e *Endpoint) { e.URL = "" }, true, false},
		{"URL に scheme と host がない", func(e *Endpoint) { e.URL = "not a url" }, true, false},
		{"購読の形式が不正", func(e *Endpoint) { e.EventTypes = []string{"order..created"} }, true, false},
		{"シークレットが nil", func(e *Endpoint) { e.EncryptedSecret = nil }, true, false},
		{"シークレットが空", func(e *Endpoint) { e.EncryptedSecret = []byte{} }, true, false},
		{"endpointId に区切り文字", func(e *Endpoint) { e.ID = "ep#1" }, true, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := validEndpoint()
			test.modify(&e)
			err := e.Validate()
			if !test.wantErr {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidEndpoint) {
				t.Fatalf("Validate() = %v, want ErrInvalidEndpoint", err)
			}
			if test.wantKeyErr && !errors.Is(err, ErrInvalidKey) {
				t.Errorf("Validate() = %v, want ErrInvalidKey にも一致", err)
			}
		})
	}
}

// URL はクエリにトークンを含みうるので、検証のエラーに URL を入れない。
func TestEndpointValidate_ErrorOmitsURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"scheme と host がない", "hook?token=secret-marker"},
		{"解析できない", "https://example.com/%zz?token=secret-marker"},
		{"制御文字", "https://example.com/\x7f?token=secret-marker"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := validEndpoint()
			e.URL = test.url
			err := e.Validate()
			if !errors.Is(err, ErrInvalidEndpoint) {
				t.Fatalf("Validate() = %v, want ErrInvalidEndpoint", err)
			}
			if strings.Contains(err.Error(), "secret-marker") {
				t.Errorf("エラーに URL が含まれる: %v", err)
			}
		})
	}
}
