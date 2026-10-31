package core

import "time"

type RetrySchedule []time.Duration

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

func (s RetrySchedule) NextDelay(attemptCount int, random float64) (time.Duration, bool) {
	if attemptCount < 1 || attemptCount >= len(s) {
		return 0, false
	}

	delay := s[attemptCount]
	jitter := float64(delay) * 0.2 * (random - 0.5)

	return delay + time.Duration(jitter), true
}
