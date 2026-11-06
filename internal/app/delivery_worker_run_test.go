package app

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// waitTimeout はテストで待つときの上限。期待どおりならずっと早く終わる。
const waitTimeout = 5 * time.Second

// addDeliveries は testKey と同じテナント、エンドポイントで、メッセージ ID の違う配信を n 件用意し、
// 受信で返す形にして返す。同じ配信を並列に記録すると条件付き書き込みが衝突するので、メッセージを分ける。
func (f *fixture) addDeliveries(n int, prefix string) []core.QueuedDelivery {
	out := make([]core.QueuedDelivery, 0, n)
	for i := range n {
		key := testKey
		key.MessageID = fmt.Sprintf("msg_%s%d", prefix, i)
		f.messages.PutMessage(core.Message{
			TenantID:  key.TenantID,
			ID:        key.MessageID,
			Type:      "order.created",
			Payload:   testPayload,
			CreatedAt: f.clock.Now().Add(-time.Minute),
		})
		f.messages.PutDelivery(key, core.NewDelivery())
		out = append(out, core.QueuedDelivery{Key: key, Receipt: "rcpt-" + key.MessageID})
	}
	return out
}

// startRun は run（Run の呼び出し）を goroutine で動かし、戻り値を受け取るチャネルを返す。
// テストの終わりに cancel を呼び、Run が戻るのを待つ。
func startRun(t *testing.T, run func() error, cancel context.CancelFunc) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	// done の値は waitRun が受け取るので、終わったことは finished で知らせる
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- run()
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(waitTimeout):
			t.Error("Run が戻らない")
		}
	})
	return done
}

// waitRun は Run が戻るのを待ち、nil であることを確かめる。
func waitRun(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("Run が戻らない")
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(waitTimeout):
		t.Fatalf("%s を待ちきれない", what)
	}
}

func TestRun_HandlesBatchInParallel(t *testing.T) {
	f := newFixture()
	batch := f.addDeliveries(3, "p")
	f.queue.batches = [][]core.QueuedDelivery{batch}

	w := f.worker(t)
	ctx, cancel := context.WithCancel(context.Background())
	// 3 件そろうまで送信をブロックする。直列なら 1 件目が抜けられず、ctx の終わりまで進まない
	var mu sync.Mutex
	arrived := 0
	release := make(chan struct{})
	f.sender.onSend = func(context.Context, core.WebhookRequest) {
		mu.Lock()
		arrived++
		if arrived == len(batch) {
			close(release)
		}
		mu.Unlock()
		select {
		case <-release:
		case <-ctx.Done():
		}
	}

	start := time.Now()
	done := startRun(t, func() error { return w.Run(ctx) }, cancel)
	// 3 件とも Ack されるまで待つ。上限は 1 秒
	deadline := time.After(time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for len(f.queue.Acks()) < len(batch) {
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatalf("1 秒で Ack = %v, want %d 件", f.queue.Acks(), len(batch))
		}
	}
	elapsed := time.Since(start)

	cancel()
	waitRun(t, done)

	want := []string{batch[0].Receipt, batch[1].Receipt, batch[2].Receipt}
	got := f.queue.Acks()
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("Ack = %v, want %v", got, want)
	}
	if got := f.rec.Count(callWebhook); got != len(batch) {
		t.Errorf("送信 %d 回, want %d", got, len(batch))
	}
	t.Logf("3 件の処理に %v", elapsed)
}

func TestRun_WaitsForBatchBeforeNextReceive(t *testing.T) {
	f := newFixture()
	first := f.addDeliveries(1, "a")
	second := f.addDeliveries(1, "b")
	f.queue.batches = [][]core.QueuedDelivery{first, second}

	w := f.worker(t)
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	release := make(chan struct{})
	secondSent := make(chan struct{})
	f.sender.onSend = func(_ context.Context, req core.WebhookRequest) {
		switch req.Headers["webhook-id"] {
		case first[0].Key.MessageID:
			close(entered)
			<-release
		case second[0].Key.MessageID:
			close(secondSent)
		}
	}

	done := startRun(t, func() error { return w.Run(ctx) }, cancel)
	waitSignal(t, entered, "1 回目の送信")
	// 1 件目が終わらないうちに次の受信が呼ばれないことを、少し待って確かめる
	time.Sleep(50 * time.Millisecond)
	if got := f.rec.Count(callReceive); got != 1 {
		t.Errorf("1 回目の処理中に受信 %d 回, want 1", got)
	}
	close(release)
	waitSignal(t, secondSent, "2 回目の送信")

	cancel()
	waitRun(t, done)

	// 2 回目の受信は 1 回目の Ack の後
	calls := f.rec.Calls()
	firstAck := slices.Index(calls, callAck)
	receives := 0
	for i, c := range calls {
		if c != callReceive {
			continue
		}
		receives++
		if receives == 2 && (firstAck < 0 || i < firstAck) {
			t.Errorf("2 回目の受信が 1 回目の Ack より前: %v", calls)
		}
	}
	if got := f.queue.Acks(); !slices.Equal(got, []string{first[0].Receipt, second[0].Receipt}) {
		t.Errorf("Ack = %v", got)
	}
}

func TestRun_FinishesInFlightAfterCancel(t *testing.T) {
	f := newFixture()
	batch := f.addDeliveries(1, "c")
	f.queue.batches = [][]core.QueuedDelivery{batch}

	w := f.worker(t)
	ctx, cancel := context.WithCancel(context.Background())
	var sendCtxErr error
	f.sender.onSend = func(sendCtx context.Context, _ core.WebhookRequest) {
		// 送信の途中で Run の ctx をキャンセルする
		cancel()
		<-ctx.Done()
		sendCtxErr = sendCtx.Err()
	}

	done := startRun(t, func() error { return w.Run(ctx) }, cancel)
	waitRun(t, done)

	if sendCtxErr != nil {
		t.Errorf("Sender に渡った ctx がキャンセルされた: %v", sendCtxErr)
	}
	if got := f.messages.SavedAttempts(); len(got) != 1 {
		t.Errorf("SaveAttempt %d 回, want 1", len(got))
	}
	if got := f.queue.Acks(); !slices.Equal(got, []string{batch[0].Receipt}) {
		t.Errorf("Ack = %v, want %v", got, []string{batch[0].Receipt})
	}
	if got := f.rec.Count(callReceive); got != 1 {
		t.Errorf("受信 %d 回, want 1（キャンセル後に受信しない）", got)
	}
}

func TestRun_ReceiveErrorBacksOff(t *testing.T) {
	f := newFixture()
	f.queue.receiveErr = errTemp

	w := f.worker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	start := time.Now()
	done := startRun(t, func() error { return w.Run(ctx) }, cancel)
	waitRun(t, done)
	elapsed := time.Since(start)

	// 2 回目の受信の後の待ち（2 秒まで）を、ctx の期限（1.5 秒）で抜ける
	if elapsed >= 1950*time.Millisecond {
		t.Errorf("Run が戻るまで %v。ctx の期限で待ちを抜けていない", elapsed)
	}
	got := f.rec.Count(callReceive)
	if got < 1 || got > 3 {
		t.Errorf("1.5 秒で受信 %d 回, want 1〜3 回", got)
	}
	if n := f.countLogs(t, "ERROR"); n < 1 {
		t.Errorf("受信エラーのログ %d 件, want 1 件以上", n)
	}
}
