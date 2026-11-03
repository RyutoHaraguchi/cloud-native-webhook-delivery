package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"
)

// maxBodyBytes はペイロードの上限（256KB）に合わせる
const maxBodyBytes = 256 * 1024

// verification は署名の検証結果。ログに出す。
type verification string

const (
	verificationVerified   verification = "verified"
	verificationFailed     verification = "failed"
	verificationNotChecked verification = "not_checked"
)

// receiver はクエリで指定された応答を返す受信サーバー。
// 処理の順は、クエリの検査、本文の読み取り、署名の検証、回数の加算、遅延、応答。
// 400、401、405、413 はすぐに返し、回数に含めない。
type receiver struct {
	verifiers []*standardwebhooks.Webhook
	logger    *slog.Logger
	// sleep は遅延を待つ。ctx が終われば中断してエラーを返す。テストで差し替える
	sleep func(ctx context.Context, d time.Duration) error

	mu sync.Mutex
	// attempts は webhook-id と URL の組ごとの受信回数。
	// webhook-id は送信先をまたいで同じなので、URL も含めて送信先を分ける。
	// 動作確認用なので件数の上限は設けず、再起動で消えてよい
	attempts map[attemptKey]int
}

type attemptKey struct {
	webhookID  string
	requestURI string
}

func newReceiver(verifiers []*standardwebhooks.Webhook, logger *slog.Logger) *receiver {
	return &receiver{
		verifiers: verifiers,
		logger:    logger,
		sleep:     sleepContext,
		attempts:  make(map[attemptKey]int),
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 生のクエリは秘密情報を含みうるので、パスだけを出す
	attrs := []slog.Attr{
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("messageId", r.Header.Get(standardwebhooks.HeaderWebhookID)),
		slog.String("webhookTimestamp", r.Header.Get(standardwebhooks.HeaderWebhookTimestamp)),
	}

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		rc.reject(w, r, http.StatusMethodNotAllowed, "method_not_allowed", verificationNotChecked, attrs)
		return
	}

	sc, bad := parseScenario(r.URL.RawQuery)
	if bad != "" {
		rc.reject(w, r, http.StatusBadRequest, string(bad), verificationNotChecked, attrs)
		return
	}
	attrs = append(attrs,
		slog.Int("scenarioStatus", sc.status),
		slog.String("scenarioDelay", sc.delay.String()),
		slog.Int("scenarioFail", sc.fail),
	)

	// 公式ライブラリには受信したバイト列のまま渡す
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			rc.reject(w, r, http.StatusRequestEntityTooLarge, "body_too_large", verificationNotChecked, attrs)
			return
		}
		rc.reject(w, r, http.StatusBadRequest, "body_read_error", verificationNotChecked, attrs)
		return
	}
	attrs = append(attrs, slog.Int("bodyBytes", len(body)))

	if failure := rc.verify(body, r.Header); failure != "" {
		rc.reject(w, r, http.StatusUnauthorized, failure, verificationFailed, attrs)
		return
	}
	attrs = append(attrs, slog.String("verification", string(verificationVerified)))

	attempt := rc.nextAttempt(attemptKey{
		webhookID:  r.Header.Get(standardwebhooks.HeaderWebhookID),
		requestURI: r.URL.RequestURI(),
	})
	status := sc.statusFor(attempt)
	// 配信側の attemptCount とは別物なので、名前を分ける
	attrs = append(attrs, slog.Int("receivedCount", attempt), slog.Int("responseStatus", status))

	if err := rc.sleep(r.Context(), sc.delay); err != nil {
		// 送信側がタイムアウトなどで切断した。応答は届かない
		rc.logger.LogAttrs(r.Context(), slog.LevelWarn, "request canceled during delay", attrs...)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, http.StatusText(status)+"\n") // 書き込みの失敗は送信側の切断なので無視する
	rc.logger.LogAttrs(r.Context(), slog.LevelInfo, "responded", attrs...)
}

// verify は署名を検証し、失敗した理由のコードを返す。成功なら空を返す。
// ライブラリのエラー文には入力が含まれうるので、固定のコードに変換する。
func (rc *receiver) verify(body []byte, header http.Header) string {
	for _, wh := range rc.verifiers {
		err := wh.Verify(body, header)
		switch {
		case err == nil:
			return ""
		// ヘッダーと時刻の検査は鍵によらないので、他の鍵を試さない
		case errors.Is(err, standardwebhooks.ErrRequiredHeaders):
			return "missing_signature_headers"
		case errors.Is(err, standardwebhooks.ErrInvalidHeaders):
			return "invalid_signature_headers"
		case errors.Is(err, standardwebhooks.ErrMessageTooOld):
			return "timestamp_too_old"
		case errors.Is(err, standardwebhooks.ErrMessageTooNew):
			return "timestamp_too_new"
		}
	}
	return "no_matching_signature"
}

// nextAttempt は回数を 1 増やして返す。加算と読み取りをまとめて行い、並行する受信で数えずれないようにする。
func (rc *receiver) nextAttempt(key attemptKey) int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.attempts[key]++
	return rc.attempts[key]
}

// reject は理由のコードを本文に書いて応答する。入力値は書かない。
func (rc *receiver) reject(w http.ResponseWriter, r *http.Request, status int, code string, v verification, attrs []slog.Attr) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, code+"\n") // 書き込みの失敗は送信側の切断なので無視する

	attrs = append(attrs,
		slog.String("verification", string(v)),
		slog.String("reason", code),
		slog.Int("responseStatus", status),
	)
	rc.logger.LogAttrs(r.Context(), slog.LevelWarn, "rejected", attrs...)
}
