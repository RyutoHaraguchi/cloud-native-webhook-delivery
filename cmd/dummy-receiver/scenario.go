package main

import (
	"net/url"
	"strconv"
	"time"
)

const (
	// maxDelay は送信タイムアウト（30 秒）を超える遅延を試せ、受信側に長く残らない値にする
	maxDelay = 60 * time.Second
	// maxFail は送信の最大回数に合わせる
	maxFail = 8
)

// scenario はクエリで指定された応答の仕方を表す。
type scenario struct {
	// status は返すステータスコード。fail を指定したときは、最初の fail 回だけ返す
	status int
	delay  time.Duration
	// fail が 0 なら常に status を返す
	fail int
}

// reason はクエリが不正な理由を表す固定のコード。
// 入力値はログにも応答にも含めない。URL に秘密情報が入っていても残さないため。
type reason string

const (
	reasonMalformedQuery          reason = "malformed_query"
	reasonUnknownParameter        reason = "unknown_parameter"
	reasonDuplicateParameter      reason = "duplicate_parameter"
	reasonEmptyValue              reason = "empty_value"
	reasonInvalidStatus           reason = "invalid_status"
	reasonInvalidDelay            reason = "invalid_delay"
	reasonInvalidFail             reason = "invalid_fail"
	reasonFailRequiresErrorStatus reason = "fail_requires_error_status"
)

// parameters はクエリ名ごとの読み取り処理。項目を足すときはここに加える。
var parameters = map[string]func(s *scenario, value string) reason{
	"status": func(s *scenario, value string) reason {
		// 1xx は最終応答ではないので受け付けない
		n, err := strconv.Atoi(value)
		if err != nil || n < 200 || n > 599 {
			return reasonInvalidStatus
		}
		s.status = n
		return ""
	},
	"delay": func(s *scenario, value string) reason {
		d, err := time.ParseDuration(value)
		if err != nil || d < 0 || d > maxDelay {
			return reasonInvalidDelay
		}
		s.delay = d
		return ""
	},
	"fail": func(s *scenario, value string) reason {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > maxFail {
			return reasonInvalidFail
		}
		s.fail = n
		return ""
	},
}

// parseScenario はクエリ文字列を読む。不正なら理由を返す。
// 書き間違いを見逃さないよう、知らない名前、重複、空の値もすべて不正とする。
func parseScenario(rawQuery string) (scenario, reason) {
	s := scenario{status: 200}

	// URL.Query は壊れた組を黙って捨てるので、ParseQuery のエラーを見る
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return scenario{}, reasonMalformedQuery
	}

	for name, vs := range values {
		parse, ok := parameters[name]
		if !ok {
			return scenario{}, reasonUnknownParameter
		}
		if len(vs) != 1 {
			return scenario{}, reasonDuplicateParameter
		}
		if vs[0] == "" {
			return scenario{}, reasonEmptyValue
		}
		if r := parse(&s, vs[0]); r != "" {
			return scenario{}, r
		}
	}

	// 2xx と組み合わせた fail は、最初の回も成功するので何も起きない
	if s.fail > 0 && s.status >= 200 && s.status <= 299 {
		return scenario{}, reasonFailRequiresErrorStatus
	}

	return s, ""
}

// statusFor は attempt 回目（1 始まり）の受信に返すステータスコードを決める。
func (s scenario) statusFor(attempt int) int {
	if s.fail > 0 && attempt > s.fail {
		return 200
	}
	return s.status
}
