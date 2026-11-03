package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// 署名は必ず core.SignatureHeader で作り、受信側の公式ライブラリで検証する。
// 公式ライブラリの Sign で署名すると、ライブラリ同士の往復しか確かめられず、
// core が仕様どおりに署名できているかの証拠にならない。

var (
	secretA = testSecret(32, 0xa)
	secretB = testSecret(32, 0xb)
	secretC = testSecret(32, 0xc) // 受信側が知らない鍵
)

type testReceiver struct {
	*receiver
	logs   *bytes.Buffer
	mu     sync.Mutex
	sleeps []time.Duration
	// sleepErr を設定すると、遅延が中断されたものとして扱う
	sleepErr error
}

func newTestReceiver(t *testing.T, secrets ...string) *testReceiver {
	t.Helper()
	verifiers, err := parseSigningSecrets(strings.Join(secrets, ","))
	if err != nil {
		t.Fatalf("parseSigningSecrets: %v", err)
	}
	logs := &bytes.Buffer{}
	tr := &testReceiver{logs: logs}
	tr.receiver = newReceiver(verifiers, slog.New(slog.NewJSONHandler(logs, nil)))
	tr.sleep = func(_ context.Context, d time.Duration) error {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		tr.sleeps = append(tr.sleeps, d)
		return tr.sleepErr
	}
	return tr
}

// signedRequest は core で署名したリクエストを作る。時刻は公式ライブラリが time.Now と比べるので、現在時刻を使う。
func signedRequest(t *testing.T, target, messageID string, body []byte, secrets ...string) *http.Request {
	t.Helper()
	return signedRequestAt(t, target, messageID, time.Now(), body, secrets...)
}

func signedRequestAt(t *testing.T, target, messageID string, ts time.Time, body []byte, secrets ...string) *http.Request {
	t.Helper()
	sig, err := core.SignatureHeader(secrets, messageID, ts, body)
	if err != nil {
		t.Fatalf("SignatureHeader: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	req.Header.Set("webhook-id", messageID)
	req.Header.Set("webhook-timestamp", strconv.FormatInt(ts.Unix(), 10))
	req.Header.Set("webhook-signature", sig)
	return req
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var body = []byte(`{"type":"order.created","data":{"id":"o_1"}}`)

func TestReceiverAcceptsCoreSignature(t *testing.T) {
	tr := newTestReceiver(t, secretA)
	rec := serve(tr, signedRequest(t, "/hook", "msg_1", body, secretA))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(tr.logs.String(), `"verification":"verified"`) {
		t.Errorf("ログに verified がない: %s", tr.logs.String())
	}
}

func TestReceiverAcceptsAnyConfiguredSecret(t *testing.T) {
	tr := newTestReceiver(t, secretA, secretB)
	rec := serve(tr, signedRequest(t, "/hook", "msg_1", body, secretB))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

func TestReceiverAcceptsRotatedSignatures(t *testing.T) {
	// シークレットの更新中は新旧の署名が並ぶ。受信側が新しい方だけを知っていても通る
	tr := newTestReceiver(t, secretB)
	rec := serve(tr, signedRequest(t, "/hook", "msg_1", body, secretC, secretB))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

func TestReceiverRejectsBadSignature(t *testing.T) {
	tests := []struct {
		name   string
		req    func(t *testing.T) *http.Request
		reason string
	}{
		{
			name: "知らない鍵",
			req: func(t *testing.T) *http.Request {
				return signedRequest(t, "/hook", "msg_1", body, secretC)
			},
			reason: "no_matching_signature",
		},
		{
			name: "本文の改変",
			req: func(t *testing.T) *http.Request {
				req := signedRequest(t, "/hook", "msg_1", body, secretA)
				req.Body = io.NopCloser(bytes.NewReader(append(bytes.Clone(body), ' ')))
				return req
			},
			reason: "no_matching_signature",
		},
		{
			name: "webhook-id の差し替え",
			req: func(t *testing.T) *http.Request {
				req := signedRequest(t, "/hook", "msg_1", body, secretA)
				req.Header.Set("webhook-id", "msg_2")
				return req
			},
			reason: "no_matching_signature",
		},
		{
			name: "ヘッダーなし",
			req: func(t *testing.T) *http.Request {
				return httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
			},
			reason: "missing_signature_headers",
		},
		{
			name: "時刻が読めない",
			req: func(t *testing.T) *http.Request {
				req := signedRequest(t, "/hook", "msg_1", body, secretA)
				req.Header.Set("webhook-timestamp", "yesterday")
				return req
			},
			reason: "invalid_signature_headers",
		},
		{
			name: "時刻が古い",
			req: func(t *testing.T) *http.Request {
				return signedRequestAt(t, "/hook", "msg_1", time.Now().Add(-6*time.Minute), body, secretA)
			},
			reason: "timestamp_too_old",
		},
		{
			name: "時刻が新しすぎる",
			req: func(t *testing.T) *http.Request {
				return signedRequestAt(t, "/hook", "msg_1", time.Now().Add(6*time.Minute), body, secretA)
			},
			reason: "timestamp_too_new",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := newTestReceiver(t, secretA, secretB)
			// 署名不正はクエリの status より優先する
			req := tt.req(t)
			req.URL.RawQuery = "status=200&delay=5s"
			rec := serve(tr, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tt.reason {
				t.Errorf("body = %q, want %q", got, tt.reason)
			}
			if len(tr.sleeps) != 0 {
				t.Errorf("401 に遅延がかかった: %v", tr.sleeps)
			}
		})
	}
}

func TestReceiverFailThenSucceed(t *testing.T) {
	tr := newTestReceiver(t, secretA)
	target := "/hook?fail=2&status=503"

	for i, want := range []int{503, 503, 200, 200} {
		rec := serve(tr, signedRequest(t, target, "msg_1", body, secretA))
		if rec.Code != want {
			t.Errorf("%d 回目: status = %d, want %d", i+1, rec.Code, want)
		}
	}
}

func TestReceiverCountsPerMessageAndURL(t *testing.T) {
	tr := newTestReceiver(t, secretA)

	// msg_1 を 2 回受けて、失敗の回数を使い切る
	for range 2 {
		serve(tr, signedRequest(t, "/a?fail=2&status=503", "msg_1", body, secretA))
	}

	tests := []struct {
		name      string
		target    string
		messageID string
		want      int
	}{
		{name: "同じメッセージと URL", target: "/a?fail=2&status=503", messageID: "msg_1", want: 200},
		{name: "別のメッセージ", target: "/a?fail=2&status=503", messageID: "msg_2", want: 503},
		// webhook-id は送信先をまたいで同じなので、別のパスは別の送信先として数える
		{name: "別のパス", target: "/b?fail=2&status=503", messageID: "msg_1", want: 503},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(tr, signedRequest(t, tt.target, tt.messageID, body, secretA))
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestReceiverRejectedRequestsAreNotCounted(t *testing.T) {
	tr := newTestReceiver(t, secretA)
	target := "/hook?fail=1&status=503"

	// 署名不正の受信は数えないので、次の正しい受信が 1 回目になる
	serve(tr, signedRequest(t, target, "msg_1", body, secretC))

	if rec := serve(tr, signedRequest(t, target, "msg_1", body, secretA)); rec.Code != 503 {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestReceiverCountsConcurrentRequests(t *testing.T) {
	tr := newTestReceiver(t, secretA)
	target := "/hook?fail=3&status=503"

	const n = 20
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for range n {
		req := signedRequest(t, target, "msg_1", body, secretA)
		wg.Go(func() {
			codes <- serve(tr, req).Code
		})
	}
	wg.Wait()
	close(codes)

	failures := 0
	for c := range codes {
		if c == 503 {
			failures++
		}
	}
	if failures != 3 {
		t.Errorf("503 の数 = %d, want 3", failures)
	}
}

func TestReceiverRejectsBadQuery(t *testing.T) {
	tr := newTestReceiver(t, secretA)
	// 値にシークレットのような文字列を入れ、ログと応答に出ないことを確かめる
	req := signedRequest(t, "/hook?api_key=sk_live_leak", "msg_1", body, secretA)
	rec := serve(tr, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != string(reasonUnknownParameter) {
		t.Errorf("body = %q", got)
	}
	if strings.Contains(tr.logs.String(), "sk_live_leak") || strings.Contains(rec.Body.String(), "sk_live_leak") {
		t.Errorf("入力値が出力された: %s", tr.logs.String())
	}
	if !strings.Contains(tr.logs.String(), `"verification":"not_checked"`) {
		t.Errorf("ログに not_checked がない: %s", tr.logs.String())
	}

	// 400 は数えないので、同じ webhook-id の次の受信が 1 回目になる
	if rec := serve(tr, signedRequest(t, "/hook?fail=1&status=503", "msg_1", body, secretA)); rec.Code != 503 {
		t.Errorf("400 の後: status = %d, want 503", rec.Code)
	}
}

func TestReceiverLogOmitsSecretsAndBody(t *testing.T) {
	tr := newTestReceiver(t, secretA)
	serve(tr, signedRequest(t, "/hook", "msg_1", body, secretA))

	logs := tr.logs.String()
	for _, leaked := range []string{strings.TrimPrefix(secretA, "whsec_"), "order.created"} {
		if strings.Contains(logs, leaked) {
			t.Errorf("ログに %q が含まれる: %s", leaked, logs)
		}
	}
	if !strings.Contains(logs, `"bodyBytes":`+strconv.Itoa(len(body))) {
		t.Errorf("ログに本文のバイト数がない: %s", logs)
	}
}

func TestReceiverDelay(t *testing.T) {
	tr := newTestReceiver(t, secretA)
	target := "/hook?fail=1&status=503&delay=2s"

	// 遅延は失敗の後の 200 にもかかる
	for _, want := range []int{503, 200} {
		if rec := serve(tr, signedRequest(t, target, "msg_1", body, secretA)); rec.Code != want {
			t.Errorf("status = %d, want %d", rec.Code, want)
		}
	}
	if len(tr.sleeps) != 2 || tr.sleeps[0] != 2*time.Second || tr.sleeps[1] != 2*time.Second {
		t.Errorf("sleeps = %v, want [2s 2s]", tr.sleeps)
	}
}

func TestReceiverDelayCanceled(t *testing.T) {
	tr := newTestReceiver(t, secretA)
	tr.sleepErr = context.DeadlineExceeded

	rec := serve(tr, signedRequest(t, "/hook?delay=40s", "msg_1", body, secretA))

	// 送信側が切断したので、応答を書かない
	if rec.Body.Len() != 0 {
		t.Errorf("本文が書かれた: %q", rec.Body.String())
	}
	if !strings.Contains(tr.logs.String(), "request canceled during delay") {
		t.Errorf("ログに中断の記録がない: %s", tr.logs.String())
	}
}

func TestSleepContextStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if err := sleepContext(ctx, time.Minute); err == nil {
		t.Fatal("err = nil, want context.Canceled")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("中断まで %v かかった", elapsed)
	}
}

func TestReceiverRejectsNonPost(t *testing.T) {
	tr := newTestReceiver(t, secretA)
	rec := serve(tr, httptest.NewRequest(http.MethodGet, "/hook", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow = %q", got)
	}
}

func TestReceiverBodyLimit(t *testing.T) {
	tests := []struct {
		name string
		size int
		want int
	}{
		{name: "上限ちょうど", size: maxBodyBytes, want: http.StatusOK},
		{name: "上限超え", size: maxBodyBytes + 1, want: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := newTestReceiver(t, secretA)
			large := bytes.Repeat([]byte("a"), tt.size)
			rec := serve(tr, signedRequest(t, "/hook", "msg_1", large, secretA))
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}
