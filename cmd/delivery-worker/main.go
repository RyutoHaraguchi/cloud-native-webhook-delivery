// delivery-worker は配信キューから配信を受け取り、署名して送信先へ送り、結果を記録して再送を制御する。
// 処理の流れは docs/architecture.md の「delivery-worker」。
//
// SIGTERM か SIGINT を受けたら新しい受信をやめ、処理中の配信を終えてから終了する。
// AWS への接続は SDK の既定の設定（認証情報、リージョン、AWS_ENDPOINT_URL_* の接続先）を使う。
//
// 環境変数:
//   - MESSAGES_TABLE（必須）: messages テーブルの名前
//   - CONFIG_TABLE（必須）: config テーブルの名前
//   - DELIVERY_QUEUE_URL（必須）: 配信キューの URL
//   - DELIVERY_DLQ_URL（必須）: 配信の DLQ の URL
//   - MESSAGE_RETENTION（必須）: messages の項目の保持期間。time.ParseDuration 形式（例 168h）
//   - LOCAL_SECRET_KEY（必須。KMS 版ができるまで）: ローカル用 SecretCipher の鍵。32 バイトを標準 base64 にしたもの
//   - SSRF_ALLOWED_HOSTS（任意）: SSRF 検査の例外にするホスト名。カンマ区切り。既定は空
//   - RETRY_SCHEDULE（任意）: 再送の間隔。カンマ区切りの time.ParseDuration 形式で、各値は 0 より大きいこと。
//     未設定なら core.DefaultRetrySchedule
package main

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/adapters/aws"
	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/adapters/httpsender"
	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/adapters/local"
	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/app"
)

const (
	// receiveWaitTime は SQS のロングポーリングの待ち時間。SQS の上限に合わせる
	receiveWaitTime = 20 * time.Second
	// secretCacheTTL は復号した署名シークレットを保持する期間
	secretCacheTTL = 5 * time.Minute
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("delivery-worker failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}
	dynamo := dynamodb.NewFromConfig(awsCfg)
	sqsClient := sqs.NewFromConfig(awsCfg)

	messages, err := aws.NewMessageRepository(dynamo, aws.MessageRepositoryConfig{
		Table:     cfg.MessagesTable,
		Retention: cfg.Retention,
	}, time.Now)
	if err != nil {
		return fmt.Errorf("create message repository: %w", err)
	}
	endpoints, err := aws.NewEndpointRepository(dynamo, aws.EndpointRepositoryConfig{Table: cfg.ConfigTable})
	if err != nil {
		return fmt.Errorf("create endpoint repository: %w", err)
	}
	queue, err := aws.NewDeliveryQueue(sqsClient, aws.DeliveryQueueConfig{
		QueueURL: cfg.QueueURL,
		DLQURL:   cfg.DLQURL,
		WaitTime: receiveWaitTime,
	})
	if err != nil {
		return fmt.Errorf("create delivery queue: %w", err)
	}
	localCipher, err := local.NewCipher(cfg.Key, cryptorand.Reader)
	if err != nil {
		return fmt.Errorf("create cipher: %w", err)
	}

	worker, err := app.NewDeliveryWorker(app.Deps{
		Messages:  messages,
		Endpoints: endpoints,
		Cipher:    app.NewCachedCipher(localCipher, secretCacheTTL, time.Now),
		Publisher: queue,
		Consumer:  queue,
		Sender:    httpsender.New(httpsender.Config{AllowedHosts: cfg.AllowedHosts}),
		Schedule:  cfg.Schedule,
		Now:       time.Now,
		Random:    rand.Float64,
		Logger:    logger,
	})
	if err != nil {
		return fmt.Errorf("create delivery worker: %w", err)
	}

	logger.Info("delivery-worker started",
		slog.Int("allowedHosts", len(cfg.AllowedHosts)),
		slog.Int("retrySchedule", len(cfg.Schedule)),
	)
	if err := worker.Run(ctx); err != nil {
		return fmt.Errorf("run delivery worker: %w", err)
	}
	logger.Info("delivery-worker stopped")
	return nil
}
