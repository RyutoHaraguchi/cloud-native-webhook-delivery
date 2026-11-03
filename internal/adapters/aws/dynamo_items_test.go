package aws

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestKeys(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"messagePK", messagePK("t_1", "m_1"), "TENANT#t_1#MSG#m_1"},
		{"messageSK", messageSK, "META"},
		{"deliverySK", deliverySK("ep_1"), "DELIVERY#ep_1"},
		{"attemptSK", attemptSK("ep_1", 3), "ATTEMPT#ep_1#000003"},
		{"attemptSK 2 桁", attemptSK("ep_1", 12), "ATTEMPT#ep_1#000012"},
		{"endpointPK", endpointPK("t_1"), "TENANT#t_1"},
		{"endpointSK", endpointSK("ep_1"), "ENDPOINT#ep_1"},
		{"endpointSKPrefix", endpointSKPrefix, "ENDPOINT#"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}

	// 試行の番号は 0 埋めなので、文字列の大小が番号の大小と一致する
	if attemptSK("ep_1", 2) >= attemptSK("ep_1", 10) {
		t.Errorf("attemptSK(2) = %q が attemptSK(10) = %q より小さくない", attemptSK("ep_1", 2), attemptSK("ep_1", 10))
	}
}

func TestRequireAttributes(t *testing.T) {
	item := map[string]types.AttributeValue{
		"pk":     &types.AttributeValueMemberS{Value: "TENANT#t_1"},
		"status": &types.AttributeValueMemberS{Value: "active"},
		"gone":   &types.AttributeValueMemberNULL{Value: true},
	}

	t.Run("全部ある", func(t *testing.T) {
		if err := requireAttributes(item, "pk", "status"); err != nil {
			t.Errorf("err = %v, want nil", err)
		}
	})

	t.Run("1 つ欠け", func(t *testing.T) {
		err := requireAttributes(item, "pk", "missing")
		if err == nil {
			t.Fatal("err = nil, want error")
		}
		if !strings.Contains(err.Error(), "missing") {
			t.Errorf("err = %q, 属性名 missing を含まない", err)
		}
	})

	t.Run("値が NULL", func(t *testing.T) {
		err := requireAttributes(item, "gone")
		if err == nil {
			t.Fatal("err = nil, want error")
		}
		if !strings.Contains(err.Error(), "gone") {
			t.Errorf("err = %q, 属性名 gone を含まない", err)
		}
	})
}

func TestTimeRoundTrip(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	times := []time.Time{
		time.Date(2026, 11, 3, 19, 0, 5, 123456789, jst),
		time.Date(2026, 11, 3, 19, 0, 5, 0, jst),
		time.Now(),
	}
	for _, in := range times {
		s := formatTime(in)
		if !strings.HasSuffix(s, "Z") {
			t.Errorf("formatTime(%v) = %q, Z で終わらない", in, s)
		}
		if len(s) != 30 {
			t.Errorf("formatTime(%v) = %q, 長さ %d, want 30", in, s, len(s))
		}
		out, err := parseTime(s)
		if err != nil {
			t.Fatalf("parseTime(%q): %v", s, err)
		}
		if !out.Equal(in) {
			t.Errorf("往復 %v -> %q -> %v, 一致しない", in, s, out)
		}
	}

	// 10:00:05Z と 10:00:05.5Z。可変長の書式だと "Z" > "." で大小が逆になる
	earlier := time.Date(2026, 11, 3, 10, 0, 5, 0, time.UTC)
	later := time.Date(2026, 11, 3, 10, 0, 5, 500_000_000, time.UTC)
	if formatTime(earlier) >= formatTime(later) {
		t.Errorf("formatTime(%v) = %q が formatTime(%v) = %q より小さくない",
			earlier, formatTime(earlier), later, formatTime(later))
	}
}

func TestParseTimeRejectsInvalid(t *testing.T) {
	if _, err := parseTime("not-a-time"); err == nil {
		t.Error("err = nil, want error")
	}
}
