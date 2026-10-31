package core

import (
	"testing"
	"time"
)

func TestNextRetryDelay(t *testing.T) {
	// 0.5 のときジッターは 0 になるので、間隔そのものを確かめられる
	const noJitter = 0.5

	tests := []struct {
		name         string
		attemptCount int
		wantDelay    time.Duration
		wantOK       bool
	}{
		{"1 回目の失敗", 1, 5 * time.Second, true},
		{"2 回目の失敗", 2, 5 * time.Minute, true},
		{"3 回目の失敗", 3, 30 * time.Minute, true},
		{"4 回目の失敗", 4, 2 * time.Hour, true},
		{"5 回目の失敗", 5, 5 * time.Hour, true},
		{"6 回目の失敗", 6, 10 * time.Hour, true},
		{"7 回目の失敗", 7, 10 * time.Hour, true},
		{"8 回目の失敗は上限", 8, 0, false},
		{"上限を超えた値", 9, 0, false},
		{"まだ送信していない", 0, 0, false},
		{"負の値", -1, 0, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := DefaultRetrySchedule.NextDelay(test.attemptCount, noJitter)
			if got != test.wantDelay || ok != test.wantOK {
				t.Errorf("NextDelay(%d) = (%v, %v), want (%v, %v)",
					test.attemptCount, got, ok, test.wantDelay, test.wantOK)
			}
		})
	}
}

func TestNextRetryDelayJitter(t *testing.T) {
	tests := []struct {
		name   string
		random float64
		want   time.Duration
	}{
		{"乱数の最小値は -10%", 0, 4500 * time.Millisecond},
		{"乱数の中央値は 0%", 0.5, 5 * time.Second},
		{"乱数が 0.75 なら +5%", 0.75, 5250 * time.Millisecond},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := DefaultRetrySchedule.NextDelay(1, test.random)
			if !ok || got != test.want {
				t.Errorf("NextDelay(1) = (%v, %v), want (%v, true)", got, ok, test.want)
			}
		})
	}
}

func TestRetryScheduleCanBeReplaced(t *testing.T) {
	schedule := RetrySchedule{time.Second}
	const noJitter = 0.5

	if got, ok := schedule.NextDelay(1, noJitter); !ok || got != time.Second {
		t.Errorf("NextDelay(1) = (%v, %v), want (1s, true)", got, ok)
	}
	if _, ok := schedule.NextDelay(2, noJitter); ok {
		t.Error("NextDelay(2) は上限なので false を返すはず")
	}
}
