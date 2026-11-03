package core

// AttemptOutcome は 1 回の送信の結果を表す。
type AttemptOutcome string

const (
	AttemptOutcomeDelivered AttemptOutcome = "delivered"
	// AttemptOutcomeRetryableFailure は再送してよい失敗を表す。再送するかは送信回数で決まる。
	AttemptOutcomeRetryableFailure AttemptOutcome = "retryable_failure"
	// AttemptOutcomeGone は送信先が 410 を返したことを表す。再送しない。
	AttemptOutcomeGone AttemptOutcome = "gone"
	// AttemptOutcomeSSRFBlocked は SSRF 検査で送信を止めたことを表す。再送しない。
	AttemptOutcomeSSRFBlocked AttemptOutcome = "ssrf_blocked"
)

// ClassifyStatusCode は HTTP ステータスコードから送信結果を判定する。
// 2xx と 410 以外はすべて再送してよい失敗とする。
func ClassifyStatusCode(code int) AttemptOutcome {
	switch {
	case 200 <= code && code <= 299:
		return AttemptOutcomeDelivered
	case code == 410:
		return AttemptOutcomeGone
	default:
		return AttemptOutcomeRetryableFailure
	}
}
