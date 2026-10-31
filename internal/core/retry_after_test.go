package core

import (
	"testing"
	"time"
)

func TestApplyRetryAfterValue(t *testing.T) {
	const statusCode = 503
	const delay = 5 * time.Second
	now := time.Date(2026, time.October, 31, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"指定なし", "", delay},
		{"予定より短い秒数", "3", delay},
		{"予定と同じ秒数", "5", delay},
		{"予定より長い秒数", "3600", time.Hour},
		{"0 秒", "0", delay},
		{"上限ちょうど", "43200", 12 * time.Hour},
		{"上限を 1 秒超える", "43201", 12 * time.Hour},
		{"掛け算で桁あふれする秒数", "9223372037", 12 * time.Hour},
		{"int64 に収まらない秒数", "99999999999999999999", 12 * time.Hour},
		{"桁あふれする数字の後ろに不正な文字", "99999999999999999999junk", delay},
		{"文字列", "abc", delay},
		{"負の秒数", "-5", delay},
		{"符号付きの秒数", "+3600", delay},
		{"小数の秒数", "1.5", delay},
		{"int64 に収まらない負の秒数", "-99999999999999999999", delay},
		{"1 時間後の日時", "Sat, 31 Oct 2026 13:00:00 GMT", time.Hour},
		{"旧形式の日時 (RFC 850)", "Saturday, 31-Oct-26 13:00:00 GMT", time.Hour},
		{"旧形式の日時 (asctime)", "Sat Oct 31 13:00:00 2026", time.Hour},
		{"過去の日時", "Sat, 31 Oct 2026 11:00:00 GMT", delay},
		{"上限を超える日時", "Sun, 01 Nov 2026 12:00:00 GMT", 12 * time.Hour},
		{"日時として読めない値", "Sat, 31 Oct 2026", delay},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ApplyRetryAfter(delay, statusCode, test.value, now); got != test.want {
				t.Errorf("ApplyRetryAfter(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestApplyRetryAfterStatusCode(t *testing.T) {
	const delay = 5 * time.Second

	tests := []struct {
		name string
		code int
		want time.Duration
	}{
		{"流量制限", 429, time.Hour},
		{"不正なゲートウェイ", 502, time.Hour},
		{"一時的な利用不可", 503, time.Hour},
		{"ゲートウェイのタイムアウト", 504, time.Hour},
		{"429 の直前", 428, delay},
		{"429 の直後", 430, delay},
		{"502 の直前", 501, delay},
		{"504 の直後", 505, delay},
		{"サーバーエラー", 500, delay},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ApplyRetryAfter(delay, test.code, "3600", time.Time{}); got != test.want {
				t.Errorf("ApplyRetryAfter(%d) = %v, want %v", test.code, got, test.want)
			}
		})
	}
}

func TestApplyRetryAfterKeepsLongerDelay(t *testing.T) {
	// 上限は指定だけにかかるので、予定の間隔が上限より長ければそのまま使う
	if got := ApplyRetryAfter(20*time.Hour, 503, "86400", time.Time{}); got != 20*time.Hour {
		t.Errorf("ApplyRetryAfter = %v, want 20h", got)
	}
}
