package core

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func validMessage() Message {
	return Message{
		TenantID:  "t_1",
		ID:        "m_1",
		Type:      "order.created",
		Payload:   []byte(`{"id":1}`),
		CreatedAt: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC),
	}
}

func TestMessageValidate(t *testing.T) {
	tests := []struct {
		name       string
		modify     func(m *Message)
		wantErr    bool
		wantKeyErr bool
	}{
		{"有効", func(m *Message) {}, false, false},
		{"ペイロードがちょうど上限", func(m *Message) { m.Payload = bytes.Repeat([]byte("a"), MaxPayloadSize) }, false, false},
		{"tenantId が空", func(m *Message) { m.TenantID = "" }, true, true},
		{"tenantId に区切り文字", func(m *Message) { m.TenantID = "t#1" }, true, true},
		{"messageId が空", func(m *Message) { m.ID = "" }, true, true},
		{"messageId に区切り文字", func(m *Message) { m.ID = "m#1" }, true, true},
		{"種類の形式が不正", func(m *Message) { m.Type = "order..created" }, true, false},
		{"ペイロードが空", func(m *Message) { m.Payload = nil }, true, false},
		{"ペイロードが上限を超える", func(m *Message) { m.Payload = bytes.Repeat([]byte("a"), MaxPayloadSize+1) }, true, false},
		{"作成時刻がゼロ", func(m *Message) { m.CreatedAt = time.Time{} }, true, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := validMessage()
			test.modify(&m)
			err := m.Validate()
			if !test.wantErr {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("Validate() = %v, want ErrInvalidMessage", err)
			}
			if test.wantKeyErr && !errors.Is(err, ErrInvalidKey) {
				t.Errorf("Validate() = %v, want ErrInvalidKey にも一致", err)
			}
		})
	}
}
