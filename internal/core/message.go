package core

import (
	"errors"
	"fmt"
	"time"
)

// MaxPayloadSize はメッセージのペイロードの上限のバイト数。
const MaxPayloadSize = 256 * 1024

// ErrInvalidMessage はメッセージの値が不正なことを表す。
var ErrInvalidMessage = errors.New("invalid message")

// Message は受け付けたイベントを表す。送信先ごとの配信はこれを参照する。
type Message struct {
	TenantID  string
	ID        string
	Type      string
	Payload   []byte
	CreatedAt time.Time
}

// Validate はメッセージの値を検証する。ID が不正なときは ErrInvalidKey にも一致する。
func (m Message) Validate() error {
	if err := ValidateID("tenantId", m.TenantID); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMessage, err)
	}
	if err := ValidateID("messageId", m.ID); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMessage, err)
	}
	if !IsValidEventType(m.Type) {
		return fmt.Errorf("%w: type %q", ErrInvalidMessage, m.Type)
	}
	if len(m.Payload) == 0 {
		return fmt.Errorf("%w: payload is empty", ErrInvalidMessage)
	}
	if len(m.Payload) > MaxPayloadSize {
		return fmt.Errorf("%w: payload size %d exceeds %d", ErrInvalidMessage, len(m.Payload), MaxPayloadSize)
	}
	if m.CreatedAt.IsZero() {
		return fmt.Errorf("%w: createdAt is zero", ErrInvalidMessage)
	}
	return nil
}
