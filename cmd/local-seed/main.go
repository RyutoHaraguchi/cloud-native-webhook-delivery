// local-seed はローカル開発用に、config テーブルへテナント t_local のエンドポイントを書く。
// compose の初期化コンテナとして、テーブルの作成後に毎回動く。同じ値で上書きするので何度実行してもよい。
// 書く内容は docs/architecture.md の「ローカルのシード」。
//
// 接続先が localhost、127.0.0.1、dynamodb 以外なら何も書かずに終了する。
// 認証情報はダミーに固定し、環境の AWS の認証情報を読まない。
//
// 環境変数（すべて必須）:
//   - CONFIG_TABLE: config テーブルの名前
//   - AWS_ENDPOINT_URL_DYNAMODB: DynamoDB Local の接続先（例 http://dynamodb:8000）
//   - LOCAL_SECRET_KEY: ローカル用 SecretCipher の鍵。32 バイトを標準 base64 にしたもの
//   - SEED_SIGNING_SECRET: エンドポイントに登録する署名シークレット。whsec_ で始まる
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/adapters/aws"
	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/adapters/local"
)

const (
	// region は DynamoDB Local が検証しないダミーのリージョン
	region = "ap-northeast-1"
	// seedTimeout はシード全体の期限
	seedTimeout = 30 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("local-seed failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), seedTimeout)
	defer cancel()

	cipher, err := local.NewCipher(cfg.Key, rand.Reader)
	if err != nil {
		return fmt.Errorf("create cipher: %w", err)
	}
	// config.LoadDefaultConfig は環境や共有ファイルの認証情報を読むので使わない
	client := dynamodb.New(dynamodb.Options{
		BaseEndpoint: awssdk.String(cfg.DynamoDBURL),
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider("local", "local", ""),
	})
	repo, err := aws.NewEndpointRepository(client, aws.EndpointRepositoryConfig{Table: cfg.Table})
	if err != nil {
		return fmt.Errorf("create endpoint repository: %w", err)
	}

	endpoints, err := seedEndpoints(ctx, cipher, cfg.SigningSecret)
	if err != nil {
		return fmt.Errorf("build seed endpoints: %w", err)
	}
	for _, e := range endpoints {
		if err := repo.Put(ctx, e); err != nil {
			return fmt.Errorf("seed endpoint: %w", err)
		}
		logger.Info("endpoint seeded",
			slog.String("tenantId", e.TenantID),
			slog.String("endpointId", e.ID),
			slog.String("status", string(e.Status)),
		)
	}
	return nil
}
