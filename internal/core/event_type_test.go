package core

import "testing"

func TestIsValidEventType(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		want      bool
	}{
		{"2 つの区切り", "order.created", true},
		{"1 つの区切り", "order", true},
		{"3 つの区切り", "order.item.created", true},
		{"使える文字をすべて含む", "Order_1.created_2", true},
		{"アンダースコアだけ", "_", true},
		{"空文字", "", false},
		{"ドットだけ", ".", false},
		{"先頭がドット", ".order", false},
		{"末尾がドット", "order.", false},
		{"ドットが連続", "order..created", false},
		{"ハイフン", "order-created", false},
		{"ワイルドカード", "order.*", false},
		{"スペース", "order created", false},
		{"末尾の改行", "order.created\n", false},
		{"先頭の改行", "\norder.created", false},
		{"全角の文字", "注文.created", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsValidEventType(test.eventType); got != test.want {
				t.Errorf("IsValidEventType(%q) = %v, want %v", test.eventType, got, test.want)
			}
		})
	}
}

func TestIsSubscribed(t *testing.T) {
	subscribed := []string{"order.created", "order.canceled"}

	tests := []struct {
		name       string
		subscribed []string
		eventType  string
		want       bool
	}{
		{"先頭に一致", subscribed, "order.created", true},
		{"末尾に一致", subscribed, "order.canceled", true},
		{"一致しない", subscribed, "user.created", false},
		{"前方だけ一致", subscribed, "order", false},
		{"後ろが長い", subscribed, "order.created.v2", false},
		{"大文字と小文字が違う", subscribed, "Order.Created", false},
		{"ワイルドカードとして扱わない", []string{"order.*"}, "order.created", false},
		{"購読なし", nil, "order.created", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsSubscribed(test.subscribed, test.eventType); got != test.want {
				t.Errorf("IsSubscribed(%v, %q) = %v, want %v", test.subscribed, test.eventType, got, test.want)
			}
		})
	}
}
