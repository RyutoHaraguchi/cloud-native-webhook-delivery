package aws

import (
	"errors"
	"testing"
	"time"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

func TestDelaySeconds(t *testing.T) {
	tests := []struct {
		name    string
		d       time.Duration
		want    int32
		wantErr error
	}{
		{"負は 0", -time.Second, 0, nil},
		{"0", 0, 0, nil},
		{"端数は切り上げ", 1200 * time.Millisecond, 2, nil},
		{"上限ちょうど", 15 * time.Minute, 900, nil},
		{"上限を 1ns 超える", 15*time.Minute + time.Nanosecond, 0, core.ErrDelayTooLong},
		{"16 分", 16 * time.Minute, 0, core.ErrDelayTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := delaySeconds(tt.d)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("delaySeconds(%s) err = %v, want %v", tt.d, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("delaySeconds(%s) err = %v, want nil", tt.d, err)
			}
			if got != tt.want {
				t.Errorf("delaySeconds(%s) = %d, want %d", tt.d, got, tt.want)
			}
		})
	}
}

func TestDecodeKey(t *testing.T) {
	key := core.DeliveryKey{TenantID: "t_1", MessageID: "m_1", EndpointID: "ep_1"}

	t.Run("往復", func(t *testing.T) {
		body, err := encodeKey(key)
		if err != nil {
			t.Fatalf("encodeKey err = %v", err)
		}
		want := `{"tenantId":"t_1","messageId":"m_1","endpointId":"ep_1"}`
		if body != want {
			t.Errorf("encodeKey = %s, want %s", body, want)
		}
		got, err := decodeKey(body)
		if err != nil {
			t.Fatalf("decodeKey err = %v", err)
		}
		if got != key {
			t.Errorf("decodeKey = %+v, want %+v", got, key)
		}
	})

	t.Run("知らないフィールドは無視する", func(t *testing.T) {
		got, err := decodeKey(`{"tenantId":"t_1","messageId":"m_1","endpointId":"ep_1","extra":1}`)
		if err != nil {
			t.Fatalf("decodeKey err = %v", err)
		}
		if got != key {
			t.Errorf("decodeKey = %+v, want %+v", got, key)
		}
	})

	errTests := []struct {
		name       string
		body       string
		wantKeyErr bool
	}{
		{"JSON でない", "not json", false},
		{"空のオブジェクト", "{}", true},
		{"tenantId に #", `{"tenantId":"t#1","messageId":"m_1","endpointId":"ep_1"}`, true},
	}
	for _, tt := range errTests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeKey(tt.body)
			if err == nil {
				t.Fatal("err = nil, want error")
			}
			if tt.wantKeyErr && !errors.Is(err, core.ErrInvalidKey) {
				t.Errorf("err = %v, want ErrInvalidKey", err)
			}
		})
	}
}

func TestNewDeliveryQueueConfig(t *testing.T) {
	valid := DeliveryQueueConfig{QueueURL: "http://q", DLQURL: "http://dlq", WaitTime: 20 * time.Second}
	tests := []struct {
		name    string
		mutate  func(*DeliveryQueueConfig)
		wantErr bool
	}{
		{"20 秒", func(*DeliveryQueueConfig) {}, false},
		{"0 秒", func(c *DeliveryQueueConfig) { c.WaitTime = 0 }, false},
		{"秒の端数", func(c *DeliveryQueueConfig) { c.WaitTime = 1500 * time.Millisecond }, true},
		{"21 秒", func(c *DeliveryQueueConfig) { c.WaitTime = 21 * time.Second }, true},
		{"負", func(c *DeliveryQueueConfig) { c.WaitTime = -time.Second }, true},
		{"QueueURL が空", func(c *DeliveryQueueConfig) { c.QueueURL = "" }, true},
		{"DLQURL が空", func(c *DeliveryQueueConfig) { c.DLQURL = "" }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			q, err := NewDeliveryQueue(nil, cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("err = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if q == nil {
				t.Fatal("queue = nil")
			}
		})
	}
}

func TestDeliveryQueue_ReceiveMax(t *testing.T) {
	// client が nil なので、SQS を呼べば panic する。呼ばずにエラーを返すことを確かめる
	q, err := NewDeliveryQueue(nil, DeliveryQueueConfig{QueueURL: "http://q", DLQURL: "http://dlq", WaitTime: time.Second})
	if err != nil {
		t.Fatalf("NewDeliveryQueue err = %v", err)
	}
	for _, max := range []int{0, 11} {
		if _, err := q.Receive(t.Context(), max); err == nil {
			t.Errorf("Receive(max=%d) err = nil, want error", max)
		}
	}
}
