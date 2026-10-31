package core

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxRetryAfter は Retry-After の指定を採用する上限を表す。
const maxRetryAfter = 12 * time.Hour

// ApplyRetryAfter は Retry-After を反映した待ち時間を返す。
// delay には NextDelay の結果を渡し、指定より短いときだけ延ばす。
// 対象外のステータスコードのときと、値を読めないときは delay をそのまま返す。
func ApplyRetryAfter(delay time.Duration, statusCode int, value string, now time.Time) time.Duration {
	switch statusCode {
	case 429, 502, 503, 504:
	default:
		return delay
	}

	retryAfter, ok := parseRetryAfter(value, now)
	if !ok {
		return delay
	}

	return max(delay, retryAfter)
}

// parseRetryAfter は秒数または日時の形式の値を、上限以下の待ち時間に変換する。読めないときは false を返す。
func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	// 桁あふれすると ParseUint は残りの文字を読まずに最大値を返すので、すべて数字か確かめる
	seconds, err := strconv.ParseUint(value, 10, 64)
	if err == nil || (errors.Is(err, strconv.ErrRange) && strings.Trim(value, "0123456789") == "") {
		// time.Second を掛けると桁あふれする値があるので、先に上限と比べる
		return time.Duration(min(seconds, uint64(maxRetryAfter/time.Second))) * time.Second, true
	}

	at, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}

	return min(at.Sub(now), maxRetryAfter), true
}
