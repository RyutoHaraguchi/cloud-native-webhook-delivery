package core

import "testing"

func TestClassifyStatusCode(t *testing.T) {
	tests := []struct {
		name string
		code int
		want AttemptOutcome
	}{
		{"2xx の下限", 200, AttemptOutcomeDelivered},
		{"2xx の上限", 299, AttemptOutcomeDelivered},
		{"2xx の直前", 199, AttemptOutcomeRetryableFailure},
		{"2xx の直後", 300, AttemptOutcomeRetryableFailure},
		{"リダイレクトは追わない", 302, AttemptOutcomeRetryableFailure},
		{"410 の直前", 409, AttemptOutcomeRetryableFailure},
		{"410 は再送しない", 410, AttemptOutcomeGone},
		{"410 の直後", 411, AttemptOutcomeRetryableFailure},
		{"流量制限", 429, AttemptOutcomeRetryableFailure},
		{"サーバーエラー", 500, AttemptOutcomeRetryableFailure},
		{"一時的な利用不可", 503, AttemptOutcomeRetryableFailure},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyStatusCode(test.code); got != test.want {
				t.Errorf("ClassifyStatusCode(%d) = %q, want %q", test.code, got, test.want)
			}
		})
	}
}
