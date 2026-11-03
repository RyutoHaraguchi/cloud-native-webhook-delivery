// dummy-receiver は動作確認用の Webhook 受信サーバー。
// 署名を Standard Webhooks の公式ライブラリで検証し、クエリで指定した応答を返す。
//
// クエリ（すべて省略可。知らない名前、重複、空の値は 400）:
//   - status: 返すステータスコード（200〜599、既定 200）
//   - delay: 応答までの遅延（Go の時間表記、0〜60s）。400 と 401 以外のすべての応答にかかる
//   - fail: 最初の fail 回（1〜8）だけ status を返し、以降は 200。2xx 以外の status と組で使う
//
// 例: /hook?fail=2&status=503&delay=2s
//
// fail の回数は webhook-id と URL（パスとクエリ）の組ごとにメモリで数える。
// webhook-id は送信先をまたいで同じなので、送信先ごとに別のパスを登録する。
// 受信側で 400 や 401 を返しても、配信側は再送して attemptCount を増やす。
//
// 環境変数:
//   - SIGNING_SECRETS: 必須。whsec_ で始まるシークレットをカンマ区切りで並べ、どれかで通れば合格
//   - PORT: 待ち受けるポート（既定 8080）
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const shutdownTimeout = 10 * time.Second

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("dummy-receiver stopped", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	verifiers, err := parseSigningSecrets(os.Getenv("SIGNING_SECRETS"))
	if err != nil {
		return err
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	server := &http.Server{
		Addr:              net.JoinHostPort("", port),
		Handler:           newReceiver(verifiers, logger),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// 遅延の上限より長くする
		WriteTimeout: maxDelay + 10*time.Second,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()
	logger.Info("dummy-receiver started", slog.String("addr", server.Addr), slog.Int("signingSecrets", len(verifiers)))

	select {
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		// 遅延中の受信が残っている。接続を閉じてリクエストの context を終わらせる
		_ = server.Close() // 閉じる処理の失敗は、終了するので扱わない
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("listen: %w", err)
	}
	return nil
}
