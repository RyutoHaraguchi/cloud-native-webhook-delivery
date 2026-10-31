package core

// AttemptOutcomeは1回の送信の結果を表す。
type AttemptOutcome string

const (
	AttemptOutcomeDelivered AttemptOutcome = "delivered"
	AttemptOutcomeRetry     AttemptOutcome = "retry"
	AttemptOutcomeGone      AttemptOutcome = "gone"
)

// ClassifyStatusCode は HTTP ステータスコードから送信結果を判定する。
func ClassifyStatusCode(code int) AttemptOutcome {
	switch {
	case 200 <= code && code <= 299:
		return AttemptOutcomeDelivered
	case code == 410:
		return AttemptOutcomeGone
	default:
		return AttemptOutcomeRetry
	}
}
