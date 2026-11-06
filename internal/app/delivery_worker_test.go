package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

var (
	testSecret  = "whsec_" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("k"), 32))
	testKey     = core.DeliveryKey{TenantID: "t_a", MessageID: "msg_1", EndpointID: "ep_1"}
	testPayload = []byte(`{"event":"payload-body-marker"}`)
	errTemp     = errors.New("temporary failure")
)

const (
	testReceipt    = "rcpt-1"
	testCiphertext = "ct-ciphertext-marker"
	testURL        = "https://example.com/hook?token=query-marker"
)

// fixture は Handle を 1 件流すための偽物一式。断りがなければ配信は pending/0 回、
// エンドポイントは active、Cipher は正しいシークレットを返し、Sender は 200 を返す。
type fixture struct {
	rec       *recorder
	messages  *fakeMessages
	endpoints *fakeEndpoints
	cipher    *stubCipher
	queue     *fakeQueue
	sender    *fakeSender
	clock     *fakeClock
	logs      *bytes.Buffer
	schedule  core.RetrySchedule
}

func newFixture() *fixture {
	rec := &recorder{}
	f := &fixture{
		rec:       rec,
		messages:  newFakeMessages(rec),
		endpoints: newFakeEndpoints(rec),
		cipher:    &stubCipher{rec: rec, plaintext: []byte(testSecret)},
		queue:     &fakeQueue{rec: rec},
		sender:    &fakeSender{rec: rec, resp: core.WebhookResponse{StatusCode: 200}},
		clock:     newFakeClock(),
		logs:      &bytes.Buffer{},
		schedule:  core.DefaultRetrySchedule,
	}
	f.messages.PutMessage(core.Message{
		TenantID:  testKey.TenantID,
		ID:        testKey.MessageID,
		Type:      "order.created",
		Payload:   testPayload,
		CreatedAt: f.clock.Now().Add(-time.Minute),
	})
	f.messages.PutDelivery(testKey, core.NewDelivery())
	f.endpoints.Put(core.Endpoint{
		TenantID:        testKey.TenantID,
		ID:              testKey.EndpointID,
		URL:             testURL,
		EncryptedSecret: []byte(testCiphertext),
		Status:          core.EndpointStatusActive,
	})
	return f
}

func (f *fixture) worker(t *testing.T) *DeliveryWorker {
	t.Helper()
	w, err := NewDeliveryWorker(Deps{
		Messages:  f.messages,
		Endpoints: f.endpoints,
		Cipher:    f.cipher,
		Publisher: f.queue,
		Consumer:  f.queue,
		Sender:    f.sender,
		Schedule:  f.schedule,
		Now:       f.clock.Now,
		Random:    func() float64 { return 0.5 },
		Logger:    slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatalf("NewDeliveryWorker: %v", err)
	}
	return w
}

func (f *fixture) handle(t *testing.T) {
	t.Helper()
	f.worker(t).Handle(context.Background(), core.QueuedDelivery{Key: testKey, Receipt: testReceipt})
}

// logEntries は JSON のログを 1 行ずつ読む。
func (f *fixture) logEntries(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(f.logs.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("ログを読めない %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func (f *fixture) countLogs(t *testing.T, level string) int {
	t.Helper()
	n := 0
	for _, e := range f.logEntries(t) {
		if e["level"] == level {
			n++
		}
	}
	return n
}

func restore(t *testing.T, status core.DeliveryStatus, count int, next time.Time, reason core.FailureReason) core.Delivery {
	t.Helper()
	d, err := core.RestoreDelivery(status, count, count, next, reason)
	if err != nil {
		t.Fatalf("RestoreDelivery: %v", err)
	}
	return d
}

func assertCalls(t *testing.T, f *fixture, want ...string) {
	t.Helper()
	if got := f.rec.Calls(); !slices.Equal(got, want) {
		t.Errorf("呼び出し = %v, want %v", got, want)
	}
}

func assertAcked(t *testing.T, f *fixture) {
	t.Helper()
	if got := f.queue.Acks(); !slices.Equal(got, []string{testReceipt}) {
		t.Errorf("Ack = %v, want [%s]", got, testReceipt)
	}
}

func assertNotAcked(t *testing.T, f *fixture) {
	t.Helper()
	if n := f.rec.Count(callAck); n != 0 {
		t.Errorf("Ack の回数 = %d, want 0", n)
	}
}

func onlyAttempt(t *testing.T, f *fixture) savedAttempt {
	t.Helper()
	got := f.messages.SavedAttempts()
	if len(got) != 1 {
		t.Fatalf("SaveAttempt の回数 = %d, want 1", len(got))
	}
	return got[0]
}

func TestNewDeliveryWorker_RejectsMissingDeps(t *testing.T) {
	f := newFixture()
	full := func() Deps {
		return Deps{
			Messages: f.messages, Endpoints: f.endpoints, Cipher: f.cipher,
			Publisher: f.queue, Consumer: f.queue, Sender: f.sender,
			Now: f.clock.Now, Random: func() float64 { return 0.5 }, Logger: slog.New(slog.DiscardHandler),
		}
	}
	tests := []struct {
		name   string
		mutate func(*Deps)
	}{
		{"Messages", func(d *Deps) { d.Messages = nil }},
		{"Endpoints", func(d *Deps) { d.Endpoints = nil }},
		{"Cipher", func(d *Deps) { d.Cipher = nil }},
		{"Publisher", func(d *Deps) { d.Publisher = nil }},
		{"Consumer", func(d *Deps) { d.Consumer = nil }},
		{"Sender", func(d *Deps) { d.Sender = nil }},
		{"Now", func(d *Deps) { d.Now = nil }},
		{"Random", func(d *Deps) { d.Random = nil }},
		{"Logger", func(d *Deps) { d.Logger = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := full()
			tt.mutate(&d)
			if _, err := NewDeliveryWorker(d); err == nil {
				t.Error("err = nil, want エラー")
			}
		})
	}

	// Schedule は nil でよい（再送なし）
	if _, err := NewDeliveryWorker(full()); err != nil {
		t.Errorf("Schedule が nil で err = %v", err)
	}
}

func TestHandle_QueuedErrNotAcked(t *testing.T) {
	f := newFixture()
	f.worker(t).Handle(context.Background(), core.QueuedDelivery{Receipt: testReceipt, Err: errors.New("bad body")})

	assertNotAcked(t, f)
	if n := f.rec.Count(callGetDelivery); n != 0 {
		t.Errorf("GetDelivery の回数 = %d, want 0", n)
	}
}

func TestHandle_TerminalStatesAck(t *testing.T) {
	tests := []struct {
		name   string
		status core.DeliveryStatus
		reason core.FailureReason
	}{
		{"delivered", core.DeliveryStatusDelivered, ""},
		{"canceled", core.DeliveryStatusCanceled, ""},
		{"failed/gone", core.DeliveryStatusFailed, core.FailureReasonGone},
		{"failed/ssrf_blocked", core.DeliveryStatusFailed, core.FailureReasonSSRFBlocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture()
			f.messages.PutDelivery(testKey, restore(t, tt.status, 1, time.Time{}, tt.reason))
			f.handle(t)

			assertCalls(t, f, callGetDelivery, callAck)
			assertAcked(t, f)
		})
	}
}

func TestHandle_FailedRetriesExhaustedSendsToDLQ(t *testing.T) {
	f := newFixture()
	f.messages.PutDelivery(testKey, restore(t, core.DeliveryStatusFailed, 8, time.Time{}, core.FailureReasonRetriesExhausted))
	f.handle(t)

	assertCalls(t, f, callGetDelivery, callSendToDLQ, callAck)
	if got := f.queue.DLQ(); !slices.Equal(got, []core.DeliveryKey{testKey}) {
		t.Errorf("DLQ = %v, want [%v]", got, testKey)
	}
	assertAcked(t, f)
}

func TestHandle_BeforeNextAttemptRequeues(t *testing.T) {
	f := newFixture()
	f.messages.PutDelivery(testKey, restore(t, core.DeliveryStatusRetrying, 1, f.clock.Now().Add(40*time.Second), ""))
	f.handle(t)

	assertCalls(t, f, callGetDelivery, callSend, callAck)
	if got := f.queue.Sent(); !slices.Equal(got, []sentDelivery{{Key: testKey, Delay: 40 * time.Second}}) {
		t.Errorf("Send = %v, want 40s", got)
	}
	assertAcked(t, f)
}

func TestHandle_BeforeNextAttempt_CapsAt15Minutes(t *testing.T) {
	f := newFixture()
	f.messages.PutDelivery(testKey, restore(t, core.DeliveryStatusRetrying, 1, f.clock.Now().Add(time.Hour), ""))
	f.handle(t)

	if got := f.queue.Sent(); !slices.Equal(got, []sentDelivery{{Key: testKey, Delay: 15 * time.Minute}}) {
		t.Errorf("Send = %v, want 15m", got)
	}
	assertAcked(t, f)
}

func TestHandle_RetryingDueSends(t *testing.T) {
	f := newFixture()
	prev := restore(t, core.DeliveryStatusRetrying, 1, f.clock.Now(), "")
	f.messages.PutDelivery(testKey, prev)
	f.handle(t)

	a := onlyAttempt(t, f)
	if a.Next.Status() != core.DeliveryStatusDelivered || a.Next.AttemptCount() != 2 {
		t.Errorf("next = %s/%d, want delivered/2", a.Next.Status(), a.Next.AttemptCount())
	}
	assertAcked(t, f)
}

func assertCanceled(t *testing.T, f *fixture) {
	t.Helper()
	saved := f.messages.SavedDeliveries()
	if len(saved) != 1 {
		t.Fatalf("SaveDelivery の回数 = %d, want 1", len(saved))
	}
	s := saved[0]
	if s.Key != testKey {
		t.Errorf("key = %v, want %v", s.Key, testKey)
	}
	if s.Next.Status() != core.DeliveryStatusCanceled {
		t.Errorf("next = %s, want canceled", s.Next.Status())
	}
	if s.Next.AttemptCount() != s.Prev.AttemptCount() {
		t.Errorf("送信回数 = %d, want %d", s.Next.AttemptCount(), s.Prev.AttemptCount())
	}
	if n := f.rec.Count(callWebhook); n != 0 {
		t.Errorf("Sender の回数 = %d, want 0", n)
	}
	assertAcked(t, f)
}

func TestHandle_EndpointNotFoundCancels(t *testing.T) {
	f := newFixture()
	f.endpoints = newFakeEndpoints(f.rec)
	f.handle(t)

	assertCanceled(t, f)
}

func TestHandle_EndpointDisabledCancels(t *testing.T) {
	f := newFixture()
	f.messages.PutDelivery(testKey, restore(t, core.DeliveryStatusRetrying, 2, f.clock.Now().Add(-time.Second), ""))
	f.endpoints.Put(core.Endpoint{
		TenantID: testKey.TenantID, ID: testKey.EndpointID, URL: testURL,
		EncryptedSecret: []byte(testCiphertext), Status: core.EndpointStatusDisabled,
	})
	f.handle(t)

	assertCanceled(t, f)
}

func TestHandle_CancelConflictNotAcked(t *testing.T) {
	f := newFixture()
	f.endpoints = newFakeEndpoints(f.rec)
	f.messages.saveDeliveryErr = fmt.Errorf("save: %w", core.ErrConflict)
	f.handle(t)

	assertNotAcked(t, f)
}

func TestHandle_DeliversWithSignature(t *testing.T) {
	f := newFixture()
	// 復号に 1 秒、送信に 2 秒かかる。送信を始めた時刻は復号の後
	f.cipher.onDecrypt = func() { f.clock.Advance(time.Second) }
	start := f.clock.Now().Add(time.Second)
	f.sender.onSend = func(context.Context, core.WebhookRequest) { f.clock.Advance(2 * time.Second) }
	f.handle(t)

	reqs := f.sender.Requests()
	if len(reqs) != 1 {
		t.Fatalf("Sender の回数 = %d, want 1", len(reqs))
	}
	req := reqs[0]
	if req.URL != testURL {
		t.Errorf("URL = %q, want %q", req.URL, testURL)
	}
	if !bytes.Equal(req.Body, testPayload) {
		t.Errorf("本文 = %q, want %q", req.Body, testPayload)
	}
	wantSig, err := core.SignatureHeader([]string{testSecret}, testKey.MessageID, start, testPayload)
	if err != nil {
		t.Fatalf("SignatureHeader: %v", err)
	}
	wantHeaders := map[string]string{
		"Content-Type":      "application/json",
		"User-Agent":        "cloud-native-webhook-delivery",
		"webhook-id":        testKey.MessageID,
		"webhook-timestamp": strconv.FormatInt(start.Unix(), 10),
		"webhook-signature": wantSig,
	}
	if len(req.Headers) != len(wantHeaders) {
		t.Errorf("ヘッダー = %v, want %v", req.Headers, wantHeaders)
	}
	for k, v := range wantHeaders {
		if req.Headers[k] != v {
			t.Errorf("ヘッダー %s = %q, want %q", k, req.Headers[k], v)
		}
	}

	decrypts := f.cipher.Decrypts()
	if len(decrypts) != 1 {
		t.Fatalf("Decrypt の回数 = %d, want 1", len(decrypts))
	}
	if want := (core.SecretContext{TenantID: testKey.TenantID, EndpointID: testKey.EndpointID}); decrypts[0].SC != want {
		t.Errorf("sc = %v, want %v", decrypts[0].SC, want)
	}
	if string(decrypts[0].Ciphertext) != testCiphertext {
		t.Errorf("暗号文 = %q, want %q", decrypts[0].Ciphertext, testCiphertext)
	}

	a := onlyAttempt(t, f)
	if a.Key != testKey || a.Prev.Status() != core.DeliveryStatusPending {
		t.Errorf("key, prev = %v, %s", a.Key, a.Prev.Status())
	}
	if a.Next.Status() != core.DeliveryStatusDelivered || a.Next.AttemptCount() != 1 {
		t.Errorf("next = %s/%d, want delivered/1", a.Next.Status(), a.Next.AttemptCount())
	}
	wantAttempt := core.Attempt{
		AttemptedAt: start.Add(2 * time.Second),
		Outcome:     core.AttemptOutcomeDelivered,
		StatusCode:  200,
		Duration:    2 * time.Second,
	}
	if a.Attempt != wantAttempt {
		t.Errorf("attempt = %+v, want %+v", a.Attempt, wantAttempt)
	}
	assertCalls(t, f, callGetDelivery, callGetEndpoint, callGetMessage, callDecrypt, callWebhook, callSaveAttempt, callAck)
	assertAcked(t, f)
}

func TestHandle_503RecordsRetryingAndRequeues(t *testing.T) {
	f := newFixture()
	f.schedule = core.RetrySchedule{5 * time.Second}
	f.sender.resp = core.WebhookResponse{StatusCode: 503}
	f.handle(t)

	a := onlyAttempt(t, f)
	if a.Next.Status() != core.DeliveryStatusRetrying {
		t.Errorf("next = %s, want retrying", a.Next.Status())
	}
	if a.Attempt.Outcome != core.AttemptOutcomeRetryableFailure || a.Attempt.StatusCode != 503 {
		t.Errorf("attempt = %+v", a.Attempt)
	}
	if got := f.queue.Sent(); !slices.Equal(got, []sentDelivery{{Key: testKey, Delay: 5 * time.Second}}) {
		t.Errorf("Send = %v, want 5s", got)
	}
	assertCalls(t, f, callGetDelivery, callGetEndpoint, callGetMessage, callDecrypt, callWebhook, callSaveAttempt, callSend, callAck)
}

func TestHandle_RetryAfterLong_Requeues15Minutes(t *testing.T) {
	f := newFixture()
	f.schedule = core.RetrySchedule{5 * time.Second}
	f.sender.resp = core.WebhookResponse{StatusCode: 503, RetryAfter: "3600"}
	f.handle(t)

	if got := f.queue.Sent(); !slices.Equal(got, []sentDelivery{{Key: testKey, Delay: 15 * time.Minute}}) {
		t.Errorf("Send = %v, want 15m", got)
	}
	assertAcked(t, f)
}

func TestHandle_RetriesExhaustedSendsToDLQ(t *testing.T) {
	f := newFixture()
	f.schedule = nil
	f.sender.resp = core.WebhookResponse{StatusCode: 503}
	f.handle(t)

	a := onlyAttempt(t, f)
	if a.Next.Status() != core.DeliveryStatusFailed || a.Next.FailureReason() != core.FailureReasonRetriesExhausted {
		t.Errorf("next = %s/%s, want failed/retries_exhausted", a.Next.Status(), a.Next.FailureReason())
	}
	assertCalls(t, f, callGetDelivery, callGetEndpoint, callGetMessage, callDecrypt, callWebhook, callSaveAttempt, callSendToDLQ, callAck)
	assertAcked(t, f)
}

func TestHandle_GoneDisablesEndpoint(t *testing.T) {
	f := newFixture()
	f.sender.resp = core.WebhookResponse{StatusCode: 410}
	f.handle(t)

	a := onlyAttempt(t, f)
	if a.Next.Status() != core.DeliveryStatusFailed || a.Next.FailureReason() != core.FailureReasonGone {
		t.Errorf("next = %s/%s, want failed/gone", a.Next.Status(), a.Next.FailureReason())
	}
	if got := f.endpoints.Disabled(); !slices.Equal(got, [][2]string{{testKey.TenantID, testKey.EndpointID}}) {
		t.Errorf("Disable = %v", got)
	}
	assertCalls(t, f, callGetDelivery, callGetEndpoint, callGetMessage, callDecrypt, callWebhook, callSaveAttempt, callDisable, callAck)
	assertAcked(t, f)
}

func TestHandle_GoneDisableFailsStillAcks(t *testing.T) {
	f := newFixture()
	f.sender.resp = core.WebhookResponse{StatusCode: 410}
	f.endpoints.disableErr = errTemp
	f.handle(t)

	assertAcked(t, f)
	if n := f.countLogs(t, "ERROR"); n != 1 {
		t.Errorf("エラーログ = %d 件, want 1", n)
	}
}

func TestHandle_SSRFBlockedFails(t *testing.T) {
	f := newFixture()
	f.sender.err = fmt.Errorf("%w: x", core.ErrSSRFBlocked)
	f.handle(t)

	a := onlyAttempt(t, f)
	if a.Next.Status() != core.DeliveryStatusFailed || a.Next.FailureReason() != core.FailureReasonSSRFBlocked {
		t.Errorf("next = %s/%s, want failed/ssrf_blocked", a.Next.Status(), a.Next.FailureReason())
	}
	if a.Attempt.Outcome != core.AttemptOutcomeSSRFBlocked || a.Attempt.StatusCode != 0 {
		t.Errorf("attempt = %+v", a.Attempt)
	}
	if n := f.rec.Count(callSend) + f.rec.Count(callSendToDLQ); n != 0 {
		t.Errorf("Send と SendToDLQ の回数 = %d, want 0", n)
	}
	assertAcked(t, f)
}

func TestHandle_SendErrorIsRetryable(t *testing.T) {
	f := newFixture()
	f.sender.err = errors.New("dial: connection refused")
	f.handle(t)

	a := onlyAttempt(t, f)
	if a.Attempt.Outcome != core.AttemptOutcomeRetryableFailure || a.Attempt.StatusCode != 0 {
		t.Errorf("attempt = %+v, want retryable_failure/0", a.Attempt)
	}
	if a.Next.Status() != core.DeliveryStatusRetrying {
		t.Errorf("next = %s, want retrying", a.Next.Status())
	}
	assertAcked(t, f)
}

func TestHandle_SaveAttemptConflictNotAcked(t *testing.T) {
	f := newFixture()
	f.schedule = core.RetrySchedule{5 * time.Second}
	f.sender.resp = core.WebhookResponse{StatusCode: 503}
	f.messages.saveAttemptErr = fmt.Errorf("save: %w", core.ErrConflict)
	f.handle(t)

	assertNotAcked(t, f)
	if n := f.rec.Count(callSend); n != 0 {
		t.Errorf("Send の回数 = %d, want 0", n)
	}
}

func TestHandle_LeavesMessage(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*fixture)
		permanent bool
	}{
		{"GetDelivery の一時的エラー", func(f *fixture) { f.messages.getDeliveryErr = errTemp }, false},
		{"GetDelivery の ErrNotFound", func(f *fixture) { f.messages.getDeliveryErr = core.ErrNotFound }, true},
		{"GetMessage の ErrNotFound", func(f *fixture) { f.messages.getMessageErr = core.ErrNotFound }, true},
		{"Decrypt の ErrUndecryptable", func(f *fixture) { f.cipher.err = core.ErrUndecryptable }, true},
		{"Decrypt の一時的エラー", func(f *fixture) { f.cipher.err = errTemp }, false},
		{"SignatureHeader のエラー", func(f *fixture) { f.cipher.plaintext = []byte("not-a-secret") }, true},
		{"固定ヘッダーがある", func(f *fixture) {
			f.endpoints.Put(core.Endpoint{
				TenantID: testKey.TenantID, ID: testKey.EndpointID, URL: testURL,
				EncryptedSecret: []byte(testCiphertext), EncryptedHeaders: []byte("hdr"),
				Status: core.EndpointStatusActive,
			})
		}, true},
		{"Endpoints.Get の一時的エラー", func(f *fixture) { f.endpoints.getErr = errTemp }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture()
			tt.setup(f)
			f.handle(t)

			assertNotAcked(t, f)
			if n := f.rec.Count(callSaveAttempt); n != 0 {
				t.Errorf("SaveAttempt の回数 = %d, want 0", n)
			}
			if n := f.rec.Count(callWebhook); n != 0 {
				t.Errorf("Sender の回数 = %d, want 0", n)
			}

			wantMsg := "delivery left for retry"
			if tt.permanent {
				wantMsg = "delivery isolated"
			}
			entries := f.logEntries(t)
			if len(entries) != 1 || entries[0]["msg"] != wantMsg {
				t.Errorf("ログ = %v, want msg %q を 1 件", entries, wantMsg)
			}
		})
	}
}

func TestHandle_RequeueFailureNotAcked(t *testing.T) {
	f := newFixture()
	f.schedule = core.RetrySchedule{5 * time.Second}
	f.sender.resp = core.WebhookResponse{StatusCode: 503}
	f.queue.sendErr = errTemp
	f.handle(t)

	onlyAttempt(t, f)
	assertNotAcked(t, f)
}

func TestHandle_DLQFailureNotAcked(t *testing.T) {
	f := newFixture()
	f.messages.PutDelivery(testKey, restore(t, core.DeliveryStatusFailed, 8, time.Time{}, core.FailureReasonRetriesExhausted))
	f.queue.dlqErr = errTemp
	f.handle(t)

	assertNotAcked(t, f)
}

func TestHandle_BeforeNextAttemptRequeueFailureNotAcked(t *testing.T) {
	f := newFixture()
	f.messages.PutDelivery(testKey, restore(t, core.DeliveryStatusRetrying, 1, f.clock.Now().Add(40*time.Second), ""))
	f.queue.sendErr = errTemp
	f.handle(t)

	assertCalls(t, f, callGetDelivery, callSend)
	assertNotAcked(t, f)
}

func TestHandle_RetriesExhaustedDLQFailureNotAcked(t *testing.T) {
	f := newFixture()
	f.schedule = nil
	f.sender.resp = core.WebhookResponse{StatusCode: 503}
	f.queue.dlqErr = errTemp
	f.handle(t)

	onlyAttempt(t, f)
	if n := f.rec.Count(callSendToDLQ); n != 1 {
		t.Errorf("SendToDLQ の回数 = %d, want 1", n)
	}
	assertNotAcked(t, f)
}

func TestHandle_InvalidKeyNotAcked(t *testing.T) {
	f := newFixture()
	key := testKey
	key.MessageID = "msg#1"
	f.worker(t).Handle(context.Background(), core.QueuedDelivery{Key: key, Receipt: testReceipt})

	// リポジトリもキューも呼ばない
	assertCalls(t, f)
	entries := f.logEntries(t)
	if len(entries) != 1 || entries[0]["msg"] != "delivery isolated" {
		t.Errorf("ログ = %v, want msg %q を 1 件", entries, "delivery isolated")
	}
}

func TestHandle_AckFailureLogged(t *testing.T) {
	f := newFixture()
	f.queue.ackErr = errTemp
	f.handle(t)

	if n := f.rec.Count(callAck); n != 1 {
		t.Errorf("Ack の回数 = %d, want 1", n)
	}
	if n := f.countLogs(t, "ERROR"); n != 1 {
		t.Errorf("エラーログ = %d 件, want 1", n)
	}
}

func TestHandle_LogsDoNotContainSecret(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fixture)
	}{
		{"成功", func(*fixture) {}},
		{"503", func(f *fixture) { f.sender.resp = core.WebhookResponse{StatusCode: 503} }},
		{"ErrUndecryptable", func(f *fixture) { f.cipher.err = core.ErrUndecryptable }},
		{"SignatureHeader のエラー", func(f *fixture) { f.cipher.plaintext = []byte("not-a-secret") }},
		{"壊れた URL のエンドポイント", func(f *fixture) {
			// アダプタは読んだエンドポイントを Validate し、そのエラーを返す
			broken := core.Endpoint{
				TenantID: testKey.TenantID, ID: testKey.EndpointID, URL: "hook?token=secret-marker",
				EncryptedSecret: []byte(testCiphertext), Status: core.EndpointStatusActive,
			}
			err := broken.Validate()
			if err == nil {
				panic("壊れた URL のエンドポイントが検証を通った")
			}
			f.endpoints.getErr = fmt.Errorf("get endpoint item: %w", err)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture()
			tt.setup(f)
			f.handle(t)

			out := f.logs.String()
			if out == "" {
				t.Fatal("ログが出ていない")
			}
			for _, banned := range []string{testSecret, "whsec_", "not-a-secret", testCiphertext, string(testPayload), "payload-body-marker", "query-marker", "secret-marker"} {
				if strings.Contains(out, banned) {
					t.Errorf("ログに %q が含まれる: %s", banned, out)
				}
			}
			for _, e := range f.logEntries(t) {
				if e["tenantId"] != testKey.TenantID || e["messageId"] != testKey.MessageID || e["endpointId"] != testKey.EndpointID {
					t.Errorf("ログに ID がない: %v", e)
				}
			}
		})
	}
}
