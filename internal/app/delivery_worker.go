package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// MaxQueueDelay は 1 回の再送待ちで SQS に入れる遅延の上限。これを超える待ちは刻んで入れる
const MaxQueueDelay = 15 * time.Minute

// receiveBatchSize は 1 回の受信で受け取る配信の最大数。SQS の上限に合わせる
const receiveBatchSize = 10

// receiveErrorBackoff は受信に失敗したとき、次の受信までに待つ時間。
const receiveErrorBackoff = time.Second

// handleTimeout は 1 件の処理全体の期限。SQS の可視性タイムアウト 90 秒より短くし、
// 処理中に再受信されないようにする。送信自体の期限 30 秒はこの中に収まる
const handleTimeout = 80 * time.Second

// userAgent は送信するリクエストの User-Agent。
const userAgent = "cloud-native-webhook-delivery"

// errEncryptedHeadersUnsupported は固定ヘッダーの平文の形式が決まるまで、固定ヘッダーのある送信先に送らないことを表す。
var errEncryptedHeadersUnsupported = errors.New("encrypted headers are not supported yet")

// permanentErrors は再受信しても直らないエラー。残すのは同じだが、ログで区別する。
var permanentErrors = []error{
	core.ErrNotFound,
	core.ErrInvalidKey,
	core.ErrInvalidDelivery,
	core.ErrInvalidMessage,
	core.ErrInvalidEndpoint,
	core.ErrUndecryptable,
	core.ErrInvalidSigningSecret,
	core.ErrInvalidMessageID,
	errEncryptedHeadersUnsupported,
}

// Deps は DeliveryWorker の部品。
type Deps struct {
	Messages  core.MessageRepository
	Endpoints core.EndpointRepository
	Cipher    core.SecretCipher
	Publisher core.DeliveryPublisher
	Consumer  core.DeliveryConsumer
	Sender    core.WebhookSender
	// Schedule は再送の間隔。nil（空）なら再送しない
	Schedule core.RetrySchedule
	Now      func() time.Time
	// Random は 0 以上 1 未満の乱数を返す。ジッターに使う
	Random func() float64
	Logger *slog.Logger
}

// DeliveryWorker はキューから受け取った配信を 1 件ずつ送信先へ送り、結果を記録する。
type DeliveryWorker struct {
	d Deps
	// handleTimeout と receiveBackoff は定数の値で作る。テストだけが短く差し替える
	handleTimeout  time.Duration
	receiveBackoff time.Duration
}

// NewDeliveryWorker は DeliveryWorker を作る。Schedule 以外の部品が nil ならエラーを返す。
func NewDeliveryWorker(d Deps) (*DeliveryWorker, error) {
	checks := []struct {
		name  string
		isNil bool
	}{
		{"Messages", d.Messages == nil},
		{"Endpoints", d.Endpoints == nil},
		{"Cipher", d.Cipher == nil},
		{"Publisher", d.Publisher == nil},
		{"Consumer", d.Consumer == nil},
		{"Sender", d.Sender == nil},
		{"Now", d.Now == nil},
		{"Random", d.Random == nil},
		{"Logger", d.Logger == nil},
	}
	for _, c := range checks {
		if c.isNil {
			return nil, fmt.Errorf("new delivery worker: %s is nil", c.name)
		}
	}
	return &DeliveryWorker{d: d, handleTimeout: handleTimeout, receiveBackoff: receiveErrorBackoff}, nil
}

// Run は ctx がキャンセルされるまで、配信を受信して処理し続ける。
// 1 回に受け取った分は並列に処理し、全部終えてから次を受信する。
// ctx がキャンセルされたら新しい受信をやめ、処理中の分はキャンセルしない ctx で最後まで終えてから nil を返す。
// 1 件の処理には handleTimeout の期限を付け、止まった I/O がバッチと終了を止め続けないようにする。
func (w *DeliveryWorker) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		batch, err := w.d.Consumer.Receive(ctx, receiveBatchSize)
		if err != nil {
			if ctx.Err() != nil {
				// キャンセルで受信が中断されただけなので、エラーとして扱わない
				return nil
			}
			w.d.Logger.ErrorContext(ctx, "receive deliveries failed", "error", err)
			if !sleepCtx(ctx, w.receiveBackoff) {
				return nil
			}
			continue
		}

		// 処理中の分は SIGTERM で中断せず、結果の記録と Ack まで終える
		base := context.WithoutCancel(ctx)
		var wg sync.WaitGroup
		for _, q := range batch {
			wg.Go(func() {
				hctx, cancel := context.WithTimeout(base, w.handleTimeout)
				defer cancel()
				w.Handle(hctx, q)
			})
		}
		wg.Wait()
	}
	return nil
}

// sleepCtx は d だけ待つ。ctx が先に終わったら false を返す。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Handle は受け取った配信を 1 件処理する。処理を終えたら Ack し、終えられなければ Ack せずに残す。
// 残したメッセージは可視性タイムアウトの後に再受信され、受信回数の上限で DLQ に移る。
func (w *DeliveryWorker) Handle(ctx context.Context, q core.QueuedDelivery) {
	if q.Err != nil {
		// 本文を読めないので鍵がない。受信回数の上限で DLQ に移る
		w.d.Logger.ErrorContext(ctx, "queued delivery unreadable", "error", q.Err)
		return
	}

	log := w.d.Logger.With(
		"tenantId", q.Key.TenantID,
		"messageId", q.Key.MessageID,
		"endpointId", q.Key.EndpointID,
	)

	if err := w.process(ctx, log, q.Key); err != nil {
		if isPermanent(err) {
			log.ErrorContext(ctx, "delivery isolated", "error", err)
		} else {
			log.WarnContext(ctx, "delivery left for retry", "error", err)
		}
		return
	}

	if err := w.d.Consumer.Ack(ctx, q.Receipt); err != nil {
		// 再受信時に手順 2〜4 が今の状態で片付けるので、ログだけ出す
		log.ErrorContext(ctx, "ack failed", "error", err)
	}
}

func isPermanent(err error) bool {
	for _, target := range permanentErrors {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// process は配信を進める。nil を返したら Ack してよく、エラーならメッセージを残す。
func (w *DeliveryWorker) process(ctx context.Context, log *slog.Logger, key core.DeliveryKey) error {
	if err := key.Validate(); err != nil {
		return fmt.Errorf("validate key: %w", err)
	}

	d, err := w.d.Messages.GetDelivery(ctx, key)
	if err != nil {
		return fmt.Errorf("get delivery: %w", err)
	}

	// 手順 2、3: 終わった配信を片付ける
	switch d.Status() {
	case core.DeliveryStatusDelivered, core.DeliveryStatusCanceled:
		return nil
	case core.DeliveryStatusFailed:
		if d.FailureReason() == core.FailureReasonRetriesExhausted {
			return w.sendToDLQ(ctx, key)
		}
		return nil
	}

	// 手順 4: 再送時刻の前なら待ち直す
	now := w.d.Now()
	if now.Before(d.NextAttemptAt()) {
		return w.requeue(ctx, key, d.NextAttemptAt().Sub(now))
	}

	// 手順 5: 送信先がなければ取り消す
	ep, err := w.d.Endpoints.Get(ctx, key.TenantID, key.EndpointID)
	if errors.Is(err, core.ErrNotFound) {
		return w.cancel(ctx, log, key, d, "endpoint not found")
	}
	if err != nil {
		return fmt.Errorf("get endpoint: %w", err)
	}
	if ep.Status == core.EndpointStatusDisabled {
		return w.cancel(ctx, log, key, d, "endpoint disabled")
	}
	if len(ep.EncryptedHeaders) > 0 {
		return errEncryptedHeadersUnsupported
	}

	// 手順 7: 署名して送る
	req, started, err := w.buildRequest(ctx, key, ep)
	if err != nil {
		return err
	}
	resp, sendErr := w.d.Sender.Send(ctx, req)
	finished := w.d.Now()

	// 手順 8: 結果を記録する
	result := toAttemptResult(resp, sendErr)
	if sendErr != nil {
		// 送信の失敗は配信の結果として記録するので、ここではログだけ出す
		log.WarnContext(ctx, "webhook send failed", "outcome", result.Outcome, "error", sendErr)
	}
	next, err := d.RecordAttempt(result, w.d.Schedule, finished, w.d.Random())
	if err != nil {
		return fmt.Errorf("record attempt: %w", err)
	}
	attempt := core.Attempt{
		AttemptedAt: finished,
		Outcome:     result.Outcome,
		StatusCode:  result.StatusCode,
		Duration:    finished.Sub(started),
	}
	if err := w.d.Messages.SaveAttempt(ctx, key, d, next, attempt); err != nil {
		return fmt.Errorf("save attempt: %w", err)
	}
	log.InfoContext(ctx, "delivery attempted",
		"outcome", result.Outcome,
		"statusCode", result.StatusCode,
		"attemptCount", next.AttemptCount(),
		"status", next.Status(),
		"failureReason", next.FailureReason(),
	)

	// 手順 9: 新しい状態で分ける
	switch next.Status() {
	case core.DeliveryStatusRetrying:
		return w.requeue(ctx, key, next.NextAttemptAt().Sub(finished))
	case core.DeliveryStatusFailed:
		switch next.FailureReason() {
		case core.FailureReasonRetriesExhausted:
			return w.sendToDLQ(ctx, key)
		case core.FailureReasonGone:
			if err := w.d.Endpoints.Disable(ctx, key.TenantID, key.EndpointID); err != nil {
				// 配信の記録は済んでいる。次の配信がまた 410 を受けて無効にし直すので、ログだけ出して Ack する
				log.ErrorContext(ctx, "disable endpoint failed", "error", err)
			}
		}
	}
	return nil
}

// buildRequest はメッセージを取得し、シークレットを復号して署名したリクエストと、送信を始めた時刻を返す。
// 送信を始めた時刻は復号の後に取り、署名と webhook-timestamp に使う。
func (w *DeliveryWorker) buildRequest(ctx context.Context, key core.DeliveryKey, ep core.Endpoint) (core.WebhookRequest, time.Time, error) {
	msg, err := w.d.Messages.GetMessage(ctx, key.TenantID, key.MessageID)
	if err != nil {
		return core.WebhookRequest{}, time.Time{}, fmt.Errorf("get message: %w", err)
	}

	sc := core.SecretContext{TenantID: key.TenantID, EndpointID: key.EndpointID}
	secret, err := w.d.Cipher.Decrypt(ctx, sc, ep.EncryptedSecret)
	if err != nil {
		return core.WebhookRequest{}, time.Time{}, fmt.Errorf("decrypt secret: %w", err)
	}

	now := w.d.Now()
	// SignatureHeader のエラーは番兵エラーだけで、シークレットの値を含まない
	signature, err := core.SignatureHeader([]string{string(secret)}, key.MessageID, now, msg.Payload)
	if err != nil {
		return core.WebhookRequest{}, time.Time{}, fmt.Errorf("sign: %w", err)
	}

	return core.WebhookRequest{
		URL: ep.URL,
		Headers: map[string]string{
			"Content-Type":      "application/json",
			"User-Agent":        userAgent,
			"webhook-id":        key.MessageID,
			"webhook-timestamp": strconv.FormatInt(now.Unix(), 10),
			"webhook-signature": signature,
		},
		Body: msg.Payload,
	}, now, nil
}

// toAttemptResult は送信の結果を AttemptResult に変える。
func toAttemptResult(resp core.WebhookResponse, err error) core.AttemptResult {
	switch {
	case errors.Is(err, core.ErrSSRFBlocked):
		return core.AttemptResult{Outcome: core.AttemptOutcomeSSRFBlocked}
	case err != nil:
		return core.AttemptResult{Outcome: core.AttemptOutcomeRetryableFailure}
	default:
		return core.AttemptResult{
			Outcome:    core.ClassifyStatusCode(resp.StatusCode),
			StatusCode: resp.StatusCode,
			RetryAfter: resp.RetryAfter,
		}
	}
}

// cancel は配信を canceled にして記録する。
func (w *DeliveryWorker) cancel(ctx context.Context, log *slog.Logger, key core.DeliveryKey, d core.Delivery, reason string) error {
	next, err := d.Cancel()
	if err != nil {
		return fmt.Errorf("cancel delivery: %w", err)
	}
	if err := w.d.Messages.SaveDelivery(ctx, key, d, next); err != nil {
		return fmt.Errorf("save canceled delivery: %w", err)
	}
	log.InfoContext(ctx, "delivery canceled", "reason", reason)
	return nil
}

// requeue は wait だけ待ってから再受信されるように、配信をキューに入れ直す。
// SQS の遅延の上限を超える分は、再受信時に手順 4 が待ち直す。
func (w *DeliveryWorker) requeue(ctx context.Context, key core.DeliveryKey, wait time.Duration) error {
	delay := max(min(wait, MaxQueueDelay), 0)
	if err := w.d.Publisher.Send(ctx, key, delay); err != nil {
		return fmt.Errorf("requeue delivery: %w", err)
	}
	return nil
}

func (w *DeliveryWorker) sendToDLQ(ctx context.Context, key core.DeliveryKey) error {
	if err := w.d.Publisher.SendToDLQ(ctx, key); err != nil {
		return fmt.Errorf("send to dlq: %w", err)
	}
	return nil
}
