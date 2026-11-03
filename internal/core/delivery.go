package core

import (
	"errors"
	"fmt"
	"time"
)

// DeliveryStatus は配信の状態を表す。値は永続化されるので変更しない。
type DeliveryStatus string

const (
	DeliveryStatusPending   DeliveryStatus = "pending"
	DeliveryStatusRetrying  DeliveryStatus = "retrying"
	DeliveryStatusDelivered DeliveryStatus = "delivered"
	DeliveryStatusFailed    DeliveryStatus = "failed"
	DeliveryStatusCanceled  DeliveryStatus = "canceled"
)

var allowedTransitions = map[[2]DeliveryStatus]bool{
	{DeliveryStatusPending, DeliveryStatusDelivered}:  true,
	{DeliveryStatusPending, DeliveryStatusRetrying}:   true,
	{DeliveryStatusPending, DeliveryStatusFailed}:     true,
	{DeliveryStatusPending, DeliveryStatusCanceled}:   true,
	{DeliveryStatusPending, DeliveryStatusPending}:    true,
	{DeliveryStatusRetrying, DeliveryStatusDelivered}: true,
	{DeliveryStatusRetrying, DeliveryStatusRetrying}:  true,
	{DeliveryStatusRetrying, DeliveryStatusFailed}:    true,
	{DeliveryStatusRetrying, DeliveryStatusCanceled}:  true,
	{DeliveryStatusRetrying, DeliveryStatusPending}:   true,
	{DeliveryStatusFailed, DeliveryStatusPending}:     true,
}

// CanTransition は from から to へ遷移できるかを返す。
// 状態の組み合わせだけを判定し、失敗理由や送信回数は見ない。
func CanTransition(from DeliveryStatus, to DeliveryStatus) bool {
	return allowedTransitions[[2]DeliveryStatus{from, to}]
}

// FailureReason は配信が failed になった理由を表す。値は永続化されるので変更しない。
type FailureReason string

const (
	FailureReasonRetriesExhausted FailureReason = "retries_exhausted"
	FailureReasonGone             FailureReason = "gone"
	FailureReasonSSRFBlocked      FailureReason = "ssrf_blocked"
)

var (
	ErrInvalidDelivery   = errors.New("invalid delivery")
	ErrInvalidTransition = errors.New("invalid delivery transition")
	ErrUnknownOutcome    = errors.New("unknown attempt outcome")
)

// Delivery は配信の状態を表す。状態は出来事ごとのメソッドでだけ変わる。
// メソッドは元の値を変えずに新しい値を返すので、元の値の Status と AttemptCount を
// 条件付き更新の期待値に使う。
type Delivery struct {
	status        DeliveryStatus
	attemptCount  int
	attemptSeq    int
	nextAttemptAt time.Time
	failureReason FailureReason
}

// NewDelivery は作成直後の配信を返す。
func NewDelivery() Delivery {
	return Delivery{status: DeliveryStatusPending}
}

// RestoreDelivery は保存された値から配信を復元する。メソッドでは作れない組み合わせはエラーにする。
func RestoreDelivery(status DeliveryStatus, attemptCount, attemptSeq int, nextAttemptAt time.Time, failureReason FailureReason) (Delivery, error) {
	d := Delivery{
		status:        status,
		attemptCount:  attemptCount,
		attemptSeq:    attemptSeq,
		nextAttemptAt: nextAttemptAt,
		failureReason: failureReason,
	}
	if err := d.validate(); err != nil {
		return Delivery{}, fmt.Errorf("%w: %w", ErrInvalidDelivery, err)
	}

	return d, nil
}

func (d Delivery) validate() error {
	// attemptSeq は再処理でも戻さないので、attemptCount を下回らない
	if d.attemptCount < 0 || d.attemptSeq < d.attemptCount {
		return fmt.Errorf("attemptCount %d, attemptSeq %d", d.attemptCount, d.attemptSeq)
	}

	// 再送時刻を持つのは retrying だけ。ゼロ値の retrying はすぐ送信されてしまう
	if (d.status == DeliveryStatusRetrying) == d.nextAttemptAt.IsZero() {
		return fmt.Errorf("%s with nextAttemptAt %v", d.status, d.nextAttemptAt)
	}

	switch d.status {
	case DeliveryStatusPending:
		if d.attemptCount != 0 {
			return fmt.Errorf("pending with attemptCount %d", d.attemptCount)
		}
	case DeliveryStatusRetrying, DeliveryStatusDelivered:
		if d.attemptCount < 1 {
			return fmt.Errorf("%s with attemptCount %d", d.status, d.attemptCount)
		}
	case DeliveryStatusFailed:
		if d.attemptCount < 1 {
			return fmt.Errorf("failed with attemptCount %d", d.attemptCount)
		}
		switch d.failureReason {
		case FailureReasonRetriesExhausted, FailureReasonGone, FailureReasonSSRFBlocked:
		default:
			return fmt.Errorf("failed with reason %q", d.failureReason)
		}
		return nil
	case DeliveryStatusCanceled:
	default:
		return fmt.Errorf("status %q", d.status)
	}

	if d.failureReason != "" {
		return fmt.Errorf("%s with reason %q", d.status, d.failureReason)
	}

	return nil
}

func (d Delivery) Status() DeliveryStatus       { return d.status }
func (d Delivery) AttemptCount() int            { return d.attemptCount }
func (d Delivery) AttemptSeq() int              { return d.attemptSeq }
func (d Delivery) NextAttemptAt() time.Time     { return d.nextAttemptAt }
func (d Delivery) FailureReason() FailureReason { return d.failureReason }

// AttemptResult は 1 回の送信の結果を表す。
// StatusCode と RetryAfter は応答がないときはゼロ値にする。
type AttemptResult struct {
	Outcome    AttemptOutcome
	StatusCode int
	RetryAfter string
}

// RecordAttempt は送信の結果を記録した配信を返す。送信回数と試行の通し番号を 1 つ増やす。
// 再送してよい失敗は、送信回数が上限に達していれば failed、そうでなければ retrying にする。
// now には送信を終えた時刻を渡す。再送時刻の起点と、日時の形式の Retry-After の計算に使う。
// random は RetrySchedule.NextDelay に渡す。
func (d Delivery) RecordAttempt(result AttemptResult, schedule RetrySchedule, now time.Time, random float64) (Delivery, error) {
	next := Delivery{
		attemptCount: d.attemptCount + 1,
		attemptSeq:   d.attemptSeq + 1,
	}

	switch result.Outcome {
	case AttemptOutcomeDelivered:
		next.status = DeliveryStatusDelivered
	case AttemptOutcomeGone:
		next.status = DeliveryStatusFailed
		next.failureReason = FailureReasonGone
	case AttemptOutcomeSSRFBlocked:
		next.status = DeliveryStatusFailed
		next.failureReason = FailureReasonSSRFBlocked
	case AttemptOutcomeRetryableFailure:
		delay, ok := schedule.NextDelay(next.attemptCount, random)
		if !ok {
			next.status = DeliveryStatusFailed
			next.failureReason = FailureReasonRetriesExhausted
			break
		}
		next.status = DeliveryStatusRetrying
		next.nextAttemptAt = now.Add(ApplyRetryAfter(delay, result.StatusCode, result.RetryAfter, now))
	default:
		return Delivery{}, fmt.Errorf("%w: %q", ErrUnknownOutcome, result.Outcome)
	}

	return d.transitionTo(next)
}

// Cancel はエンドポイントが無効または削除済みのときに、配信を canceled にする。
// 送信しないので、送信回数と試行の通し番号は変えない。
func (d Delivery) Cancel() (Delivery, error) {
	next := d
	next.status = DeliveryStatusCanceled
	next.nextAttemptAt = time.Time{}

	return d.transitionTo(next)
}

// Reprocess は DLQ から再処理する配信を、最初から送り直せる pending に戻す。
// 対象は送信回数の上限で failed になった配信と、DynamoDB の障害などで pending や retrying のまま
// DLQ に移った配信。送信回数を 0 に戻し、再送時刻と失敗の理由を空にする。
// 試行の通し番号は履歴と重ならないように戻さない。
func (d Delivery) Reprocess() (Delivery, error) {
	// gone と ssrf_blocked は再送しても直らないので対象外
	if d.status == DeliveryStatusFailed && d.failureReason != FailureReasonRetriesExhausted {
		return Delivery{}, fmt.Errorf("%w: reprocess %s with reason %q", ErrInvalidTransition, d.status, d.failureReason)
	}

	return d.transitionTo(Delivery{
		status:     DeliveryStatusPending,
		attemptSeq: d.attemptSeq,
	})
}

func (d Delivery) transitionTo(next Delivery) (Delivery, error) {
	if !CanTransition(d.status, next.status) {
		return Delivery{}, fmt.Errorf("%w: %s to %s", ErrInvalidTransition, d.status, next.status)
	}

	return next, nil
}
