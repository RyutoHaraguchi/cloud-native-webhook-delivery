package core

import (
	"fmt"
	"strings"
)

// keySeparator は DynamoDB のキーで ID をつなぐ区切り文字。ID に含まれると別のキーと衝突する。
const keySeparator = "#"

// ValidateID は ID がキーに使えるかを検証する。空の値と # を含む値を拒否する。
// name はエラー文に使う ID の名前。
func ValidateID(name, v string) error {
	if v == "" {
		return fmt.Errorf("%w: %s is empty", ErrInvalidKey, name)
	}
	if strings.Contains(v, keySeparator) {
		return fmt.Errorf("%w: %s contains %q", ErrInvalidKey, name, keySeparator)
	}
	return nil
}

// DeliveryKey は配信を一意に指す鍵。
type DeliveryKey struct {
	TenantID   string
	MessageID  string
	EndpointID string
}

// Validate は 3 つの ID がそれぞれキーに使えるかを検証する。
func (k DeliveryKey) Validate() error {
	if err := ValidateID("tenantId", k.TenantID); err != nil {
		return err
	}
	if err := ValidateID("messageId", k.MessageID); err != nil {
		return err
	}
	return ValidateID("endpointId", k.EndpointID)
}
