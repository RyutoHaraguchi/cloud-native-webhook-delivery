package core

import "time"

// RetrySchedule は n 回目の送信の前に待つ時間の一覧を表す。要素数が送信回数の上限になる。
type RetrySchedule []time.Duration

// DefaultRetrySchedule は既定の再送間隔を表す。
var DefaultRetrySchedule = RetrySchedule{
	0,
	5 * time.Second,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	5 * time.Hour,
	10 * time.Hour,
	10 * time.Hour,
}

// NextDelay は attemptCount 回送信した後の待ち時間を返す。再送しないときは false を返す。
// random には 0 以上 1 未満の乱数を渡す。±10% のジッターに使う。
func (s RetrySchedule) NextDelay(attemptCount int, random float64) (time.Duration, bool) {
	if attemptCount < 1 || attemptCount >= len(s) {
		return 0, false
	}

	delay := s[attemptCount]
	jitter := float64(delay) * 0.2 * (random - 0.5)

	return delay + time.Duration(jitter), true
}
