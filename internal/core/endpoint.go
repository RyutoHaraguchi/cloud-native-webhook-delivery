package core

import (
	"errors"
	"fmt"
	"net/url"
)

// ErrInvalidEndpoint はエンドポイントの値が不正なことを表す。
var ErrInvalidEndpoint = errors.New("invalid endpoint")

// EndpointStatus はエンドポイントの状態を表す。値は永続化されるので変更しない。
type EndpointStatus string

const (
	EndpointStatusActive   EndpointStatus = "active"
	EndpointStatusDisabled EndpointStatus = "disabled"
)

// Endpoint はテナントが登録した送信先を表す。
// EncryptedSecret と EncryptedHeaders は暗号化済みの値で、平文は持たない。
// EncryptedHeaders は nil と空のどちらも「固定ヘッダーなし」を表す。
type Endpoint struct {
	TenantID         string
	ID               string
	URL              string
	EventTypes       []string
	EncryptedSecret  []byte
	EncryptedHeaders []byte
	Status           EndpointStatus
}

// Validate はエンドポイントの値を検証する。ID が不正なときは ErrInvalidKey にも一致する。
// URL は scheme と host があることだけを確かめる。scheme の制限は SSRF 検査と登録処理が担当する。
func (e Endpoint) Validate() error {
	if err := ValidateID("tenantId", e.TenantID); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEndpoint, err)
	}
	if err := ValidateID("endpointId", e.ID); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEndpoint, err)
	}
	// URL はクエリにトークンを含みうるので、エラーに URL も url.Error（URL を含む）も入れない
	u, err := url.Parse(e.URL)
	if err != nil {
		return fmt.Errorf("%w: url is not parseable", ErrInvalidEndpoint)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%w: url has no scheme or host", ErrInvalidEndpoint)
	}
	for _, eventType := range e.EventTypes {
		if !IsValidEventType(eventType) {
			return fmt.Errorf("%w: event type %q", ErrInvalidEndpoint, eventType)
		}
	}
	if len(e.EncryptedSecret) == 0 {
		return fmt.Errorf("%w: encryptedSecret is empty", ErrInvalidEndpoint)
	}
	switch e.Status {
	case EndpointStatusActive, EndpointStatusDisabled:
	default:
		return fmt.Errorf("%w: status %q", ErrInvalidEndpoint, e.Status)
	}
	return nil
}
