package core

// SecretContext はシークレットの暗号化コンテキスト。暗号文を特定のテナントのエンドポイントに結び付ける。
type SecretContext struct {
	TenantID   string
	EndpointID string
}

// Validate はテナント ID とエンドポイント ID がキーに使えるかを確かめる。不正なら ErrInvalidKey に一致するエラーを返す。
func (c SecretContext) Validate() error {
	if err := ValidateID("tenantId", c.TenantID); err != nil {
		return err
	}
	return ValidateID("endpointId", c.EndpointID)
}
