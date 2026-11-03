package core

import (
	"errors"
	"testing"
	"time"
)

func TestCanTransition(t *testing.T) {
	all := []DeliveryStatus{
		DeliveryStatusPending,
		DeliveryStatusRetrying,
		DeliveryStatusDelivered,
		DeliveryStatusFailed,
		DeliveryStatusCanceled,
	}

	allowed := map[[2]DeliveryStatus]bool{
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

	for _, from := range all {
		for _, to := range all {
			want := allowed[[2]DeliveryStatus{from, to}]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestCanTransitionUnknownStatus(t *testing.T) {
	if CanTransition("unknown", DeliveryStatusPending) {
		t.Error("未知の状態からの遷移を許可してはいけない")
	}
	if CanTransition(DeliveryStatusPending, "unknown") {
		t.Error("未知の状態への遷移を許可してはいけない")
	}
}

func TestDeliveryStatusValues(t *testing.T) {
	statuses := map[DeliveryStatus]string{
		DeliveryStatusPending:   "pending",
		DeliveryStatusRetrying:  "retrying",
		DeliveryStatusDelivered: "delivered",
		DeliveryStatusFailed:    "failed",
		DeliveryStatusCanceled:  "canceled",
	}

	for status, want := range statuses {
		if string(status) != want {
			t.Errorf("status = %q, want %q", status, want)
		}
	}
}

var deliveryNow = time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)

func mustRestoreDelivery(t *testing.T, status DeliveryStatus, attemptCount, attemptSeq int, failureReason FailureReason) Delivery {
	t.Helper()
	// 再送時刻を持つのは retrying だけ
	var nextAttemptAt time.Time
	if status == DeliveryStatusRetrying {
		nextAttemptAt = deliveryNow
	}
	d, err := RestoreDelivery(status, attemptCount, attemptSeq, nextAttemptAt, failureReason)
	if err != nil {
		t.Fatalf("RestoreDelivery() error = %v", err)
	}
	return d
}

func assertDelivery(t *testing.T, got Delivery, status DeliveryStatus, attemptCount, attemptSeq int, nextAttemptAt time.Time, failureReason FailureReason) {
	t.Helper()
	if got.Status() != status || got.AttemptCount() != attemptCount || got.AttemptSeq() != attemptSeq ||
		!got.NextAttemptAt().Equal(nextAttemptAt) || got.FailureReason() != failureReason {
		t.Errorf("got {%s %d %d %v %q}, want {%s %d %d %v %q}",
			got.Status(), got.AttemptCount(), got.AttemptSeq(), got.NextAttemptAt(), got.FailureReason(),
			status, attemptCount, attemptSeq, nextAttemptAt, failureReason)
	}
}

func TestNewDelivery(t *testing.T) {
	assertDelivery(t, NewDelivery(), DeliveryStatusPending, 0, 0, time.Time{}, "")
}

func TestRecordAttempt(t *testing.T) {
	// 0.5 のときジッターは 0 になる
	const noJitter = 0.5

	tests := []struct {
		name       string
		from       Delivery
		result     AttemptResult
		wantStatus DeliveryStatus
		wantCount  int
		wantSeq    int
		wantNextAt time.Time
		wantReason FailureReason
	}{
		{"初回で届く", NewDelivery(), AttemptResult{Outcome: AttemptOutcomeDelivered, StatusCode: 200},
			DeliveryStatusDelivered, 1, 1, time.Time{}, ""},
		{"初回で 410", NewDelivery(), AttemptResult{Outcome: AttemptOutcomeGone, StatusCode: 410},
			DeliveryStatusFailed, 1, 1, time.Time{}, FailureReasonGone},
		{"初回で SSRF 拒否", NewDelivery(), AttemptResult{Outcome: AttemptOutcomeSSRFBlocked},
			DeliveryStatusFailed, 1, 1, time.Time{}, FailureReasonSSRFBlocked},
		{"初回で再送してよい失敗", NewDelivery(), AttemptResult{Outcome: AttemptOutcomeRetryableFailure, StatusCode: 500},
			DeliveryStatusRetrying, 1, 1, deliveryNow.Add(5 * time.Second), ""},
		{"Retry-After を反映する", NewDelivery(), AttemptResult{Outcome: AttemptOutcomeRetryableFailure, StatusCode: 503, RetryAfter: "3600"},
			DeliveryStatusRetrying, 1, 1, deliveryNow.Add(time.Hour), ""},
		{"対象外のステータスコードの Retry-After", NewDelivery(), AttemptResult{Outcome: AttemptOutcomeRetryableFailure, StatusCode: 500, RetryAfter: "3600"},
			DeliveryStatusRetrying, 1, 1, deliveryNow.Add(5 * time.Second), ""},
		{"予定より短い Retry-After", NewDelivery(), AttemptResult{Outcome: AttemptOutcomeRetryableFailure, StatusCode: 503, RetryAfter: "1"},
			DeliveryStatusRetrying, 1, 1, deliveryNow.Add(5 * time.Second), ""},
		{"日時の形式の Retry-After は now から数える", NewDelivery(), AttemptResult{Outcome: AttemptOutcomeRetryableFailure, StatusCode: 429, RetryAfter: "Tue, 03 Nov 2026 14:00:00 GMT"},
			DeliveryStatusRetrying, 1, 1, deliveryNow.Add(2 * time.Hour), ""},
		{"応答のない失敗", NewDelivery(), AttemptResult{Outcome: AttemptOutcomeRetryableFailure},
			DeliveryStatusRetrying, 1, 1, deliveryNow.Add(5 * time.Second), ""},
		{"再送で届く", mustRestoreDelivery(t, DeliveryStatusRetrying, 3, 3, ""), AttemptResult{Outcome: AttemptOutcomeDelivered, StatusCode: 204},
			DeliveryStatusDelivered, 4, 4, time.Time{}, ""},
		{"再送で 410", mustRestoreDelivery(t, DeliveryStatusRetrying, 3, 3, ""), AttemptResult{Outcome: AttemptOutcomeGone, StatusCode: 410},
			DeliveryStatusFailed, 4, 4, time.Time{}, FailureReasonGone},
		{"7 回目の失敗は再送する", mustRestoreDelivery(t, DeliveryStatusRetrying, 6, 6, ""), AttemptResult{Outcome: AttemptOutcomeRetryableFailure, StatusCode: 500},
			DeliveryStatusRetrying, 7, 7, deliveryNow.Add(10 * time.Hour), ""},
		{"8 回目の失敗で上限", mustRestoreDelivery(t, DeliveryStatusRetrying, 7, 7, ""), AttemptResult{Outcome: AttemptOutcomeRetryableFailure, StatusCode: 503, RetryAfter: "60"},
			DeliveryStatusFailed, 8, 8, time.Time{}, FailureReasonRetriesExhausted},
		{"再処理後は通し番号だけ続く", mustRestoreDelivery(t, DeliveryStatusPending, 0, 8, ""), AttemptResult{Outcome: AttemptOutcomeRetryableFailure, StatusCode: 500},
			DeliveryStatusRetrying, 1, 9, deliveryNow.Add(5 * time.Second), ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.from.RecordAttempt(test.result, DefaultRetrySchedule, deliveryNow, noJitter)
			if err != nil {
				t.Fatalf("RecordAttempt() error = %v", err)
			}
			assertDelivery(t, got, test.wantStatus, test.wantCount, test.wantSeq, test.wantNextAt, test.wantReason)
		})
	}
}

func TestRecordAttemptUsesSchedule(t *testing.T) {
	// 要素数 + 1 が送信回数の上限になる
	schedule := RetrySchedule{time.Second}
	failure := AttemptResult{Outcome: AttemptOutcomeRetryableFailure, StatusCode: 500}

	first, err := NewDelivery().RecordAttempt(failure, schedule, deliveryNow, 0.5)
	if err != nil {
		t.Fatalf("RecordAttempt() error = %v", err)
	}
	assertDelivery(t, first, DeliveryStatusRetrying, 1, 1, deliveryNow.Add(time.Second), "")

	second, err := first.RecordAttempt(failure, schedule, deliveryNow, 0.5)
	if err != nil {
		t.Fatalf("RecordAttempt() error = %v", err)
	}
	assertDelivery(t, second, DeliveryStatusFailed, 2, 2, time.Time{}, FailureReasonRetriesExhausted)
}

func TestRecordAttemptJitter(t *testing.T) {
	failure := AttemptResult{Outcome: AttemptOutcomeRetryableFailure, StatusCode: 500}

	got, err := NewDelivery().RecordAttempt(failure, DefaultRetrySchedule, deliveryNow, 0)
	if err != nil {
		t.Fatalf("RecordAttempt() error = %v", err)
	}
	if want := deliveryNow.Add(4500 * time.Millisecond); !got.NextAttemptAt().Equal(want) {
		t.Errorf("NextAttemptAt() = %v, want %v", got.NextAttemptAt(), want)
	}
}

func TestRecordAttemptKeepsOriginal(t *testing.T) {
	// 元の値は条件付き更新の期待値に使うので、変わってはいけない
	from := mustRestoreDelivery(t, DeliveryStatusRetrying, 2, 2, "")

	if _, err := from.RecordAttempt(AttemptResult{Outcome: AttemptOutcomeDelivered}, DefaultRetrySchedule, deliveryNow, 0.5); err != nil {
		t.Fatalf("RecordAttempt() error = %v", err)
	}
	assertDelivery(t, from, DeliveryStatusRetrying, 2, 2, deliveryNow, "")
}

func TestRecordAttemptInvalidTransition(t *testing.T) {
	tests := []struct {
		name string
		from Delivery
	}{
		{"届いた後", mustRestoreDelivery(t, DeliveryStatusDelivered, 1, 1, "")},
		{"失敗した後", mustRestoreDelivery(t, DeliveryStatusFailed, 8, 8, FailureReasonRetriesExhausted)},
		{"取り消した後", mustRestoreDelivery(t, DeliveryStatusCanceled, 2, 2, "")},
		{"ゼロ値", Delivery{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.from.RecordAttempt(AttemptResult{Outcome: AttemptOutcomeDelivered}, DefaultRetrySchedule, deliveryNow, 0.5)
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("RecordAttempt() error = %v, want %v", err, ErrInvalidTransition)
			}
			assertDelivery(t, got, "", 0, 0, time.Time{}, "")
		})
	}
}

func TestRecordAttemptUnknownOutcome(t *testing.T) {
	for _, outcome := range []AttemptOutcome{"", "unknown"} {
		got, err := NewDelivery().RecordAttempt(AttemptResult{Outcome: outcome}, DefaultRetrySchedule, deliveryNow, 0.5)
		if !errors.Is(err, ErrUnknownOutcome) {
			t.Errorf("RecordAttempt(%q) error = %v, want %v", outcome, err, ErrUnknownOutcome)
		}
		assertDelivery(t, got, "", 0, 0, time.Time{}, "")
	}
}

func TestCancel(t *testing.T) {
	retrying, err := RestoreDelivery(DeliveryStatusRetrying, 3, 5, deliveryNow, "")
	if err != nil {
		t.Fatalf("RestoreDelivery() error = %v", err)
	}

	tests := []struct {
		name      string
		from      Delivery
		wantCount int
		wantSeq   int
	}{
		{"作成直後", NewDelivery(), 0, 0},
		{"再送待ち", retrying, 3, 5},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.from.Cancel()
			if err != nil {
				t.Fatalf("Cancel() error = %v", err)
			}
			// 送信しないので回数は変えず、再送時刻は消す
			assertDelivery(t, got, DeliveryStatusCanceled, test.wantCount, test.wantSeq, time.Time{}, "")
		})
	}
}

func TestCancelInvalidTransition(t *testing.T) {
	tests := []struct {
		name string
		from Delivery
	}{
		{"届いた後", mustRestoreDelivery(t, DeliveryStatusDelivered, 1, 1, "")},
		{"失敗した後", mustRestoreDelivery(t, DeliveryStatusFailed, 1, 1, FailureReasonGone)},
		{"取り消した後", mustRestoreDelivery(t, DeliveryStatusCanceled, 1, 1, "")},
		{"ゼロ値", Delivery{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.from.Cancel()
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("Cancel() error = %v, want %v", err, ErrInvalidTransition)
			}
			assertDelivery(t, got, "", 0, 0, time.Time{}, "")
		})
	}
}

func TestReprocess(t *testing.T) {
	tests := []struct {
		name    string
		from    Delivery
		wantSeq int
	}{
		{"上限で失敗", mustRestoreDelivery(t, DeliveryStatusFailed, 8, 10, FailureReasonRetriesExhausted), 10},
		{"作成直後のまま DLQ に移った", NewDelivery(), 0},
		{"再処理後のまま DLQ に移った", mustRestoreDelivery(t, DeliveryStatusPending, 0, 8, ""), 8},
		{"再送待ちのまま DLQ に移った", mustRestoreDelivery(t, DeliveryStatusRetrying, 3, 5, ""), 5},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.from.Reprocess()
			if err != nil {
				t.Fatalf("Reprocess() error = %v", err)
			}
			// 送信回数、再送時刻、理由は消し、通し番号は戻さない
			assertDelivery(t, got, DeliveryStatusPending, 0, test.wantSeq, time.Time{}, "")
		})
	}
}

func TestReprocessInvalidTransition(t *testing.T) {
	tests := []struct {
		name string
		from Delivery
	}{
		{"410 で失敗", mustRestoreDelivery(t, DeliveryStatusFailed, 1, 1, FailureReasonGone)},
		{"SSRF 拒否で失敗", mustRestoreDelivery(t, DeliveryStatusFailed, 1, 1, FailureReasonSSRFBlocked)},
		{"届いた後", mustRestoreDelivery(t, DeliveryStatusDelivered, 1, 1, "")},
		{"取り消した後", mustRestoreDelivery(t, DeliveryStatusCanceled, 1, 1, "")},
		{"ゼロ値", Delivery{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.from.Reprocess()
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("Reprocess() error = %v, want %v", err, ErrInvalidTransition)
			}
			assertDelivery(t, got, "", 0, 0, time.Time{}, "")
		})
	}
}

func TestRestoreDelivery(t *testing.T) {
	var zero time.Time
	now := deliveryNow

	tests := []struct {
		name          string
		status        DeliveryStatus
		attemptCount  int
		attemptSeq    int
		nextAttemptAt time.Time
		reason        FailureReason
		wantErr       error
	}{
		{"作成直後", DeliveryStatusPending, 0, 0, zero, "", nil},
		{"再処理後", DeliveryStatusPending, 0, 8, zero, "", nil},
		{"再送待ち", DeliveryStatusRetrying, 3, 3, now, "", nil},
		{"届いた", DeliveryStatusDelivered, 1, 1, zero, "", nil},
		{"送信前に取り消した", DeliveryStatusCanceled, 0, 0, zero, "", nil},
		{"再送待ちから取り消した", DeliveryStatusCanceled, 3, 5, zero, "", nil},
		{"上限で失敗", DeliveryStatusFailed, 8, 8, zero, FailureReasonRetriesExhausted, nil},
		{"410 で失敗", DeliveryStatusFailed, 1, 1, zero, FailureReasonGone, nil},
		{"SSRF 拒否で失敗", DeliveryStatusFailed, 1, 1, zero, FailureReasonSSRFBlocked, nil},

		{"未知の状態", "unknown", 0, 0, zero, "", ErrInvalidDelivery},
		{"空の状態", "", 0, 0, zero, "", ErrInvalidDelivery},
		{"送信回数が負", DeliveryStatusCanceled, -1, 0, zero, "", ErrInvalidDelivery},
		{"通し番号が送信回数より小さい", DeliveryStatusRetrying, 3, 2, now, "", ErrInvalidDelivery},
		{"pending に送信回数がある", DeliveryStatusPending, 1, 1, zero, "", ErrInvalidDelivery},
		{"pending に再送時刻がある", DeliveryStatusPending, 0, 0, now, "", ErrInvalidDelivery},
		{"retrying に再送時刻がない", DeliveryStatusRetrying, 3, 3, zero, "", ErrInvalidDelivery},
		{"retrying に送信回数がない", DeliveryStatusRetrying, 0, 0, now, "", ErrInvalidDelivery},
		{"delivered に送信回数がない", DeliveryStatusDelivered, 0, 0, zero, "", ErrInvalidDelivery},
		{"delivered に再送時刻がある", DeliveryStatusDelivered, 1, 1, now, "", ErrInvalidDelivery},
		{"failed に送信回数がない", DeliveryStatusFailed, 0, 0, zero, FailureReasonGone, ErrInvalidDelivery},
		{"failed に再送時刻がある", DeliveryStatusFailed, 1, 1, now, FailureReasonGone, ErrInvalidDelivery},
		{"failed に理由がない", DeliveryStatusFailed, 1, 1, zero, "", ErrInvalidDelivery},
		{"failed に未知の理由", DeliveryStatusFailed, 1, 1, zero, "unknown", ErrInvalidDelivery},
		{"canceled に再送時刻がある", DeliveryStatusCanceled, 1, 1, now, "", ErrInvalidDelivery},
		{"pending に理由がある", DeliveryStatusPending, 0, 8, zero, FailureReasonRetriesExhausted, ErrInvalidDelivery},
		{"retrying に理由がある", DeliveryStatusRetrying, 1, 1, now, FailureReasonGone, ErrInvalidDelivery},
		{"delivered に理由がある", DeliveryStatusDelivered, 1, 1, zero, FailureReasonGone, ErrInvalidDelivery},
		{"canceled に理由がある", DeliveryStatusCanceled, 1, 1, zero, FailureReasonGone, ErrInvalidDelivery},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := RestoreDelivery(test.status, test.attemptCount, test.attemptSeq, test.nextAttemptAt, test.reason)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("RestoreDelivery() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				assertDelivery(t, got, "", 0, 0, zero, "")
				return
			}
			assertDelivery(t, got, test.status, test.attemptCount, test.attemptSeq, test.nextAttemptAt, test.reason)
		})
	}
}

func TestFailureReasonValues(t *testing.T) {
	reasons := map[FailureReason]string{
		FailureReasonRetriesExhausted: "retries_exhausted",
		FailureReasonGone:             "gone",
		FailureReasonSSRFBlocked:      "ssrf_blocked",
	}

	for reason, want := range reasons {
		if string(reason) != want {
			t.Errorf("reason = %q, want %q", reason, want)
		}
	}
}
