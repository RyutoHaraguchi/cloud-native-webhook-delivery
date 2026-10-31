# AGENTS.md

マルチテナント対応の Webhook 配信基盤。設計は `docs/architecture.md`、判断の理由は `docs/adr/` を読むこと。

## 現在の状態

実装中。`internal/core` の一部まで実装済み。以下のコマンドと構成には予定のものを含むので、該当ファイルが存在するか確認してから使う。

## 構成

- `cmd/`: `ingest-api`、`management-api`、`authorizer`、`enqueuer`、`delivery-worker`、`dummy-receiver`
- `internal/core`: クラウド非依存のドメインロジック
- `internal/adapters/aws`: AWS SDK を使う実装
- `scripts/`: テナント作成や DLQ の再処理など、運営者用のコマンド
- `infra/`: Terraform（`bootstrap`、`aws/modules`、`aws/envs/dev`）

## コマンド（予定）

```sh
go build ./...
golangci-lint run
go test ./...                       # ユニットテスト
docker compose up -d                # DynamoDB Local、ElasticMQ、dummy-receiver
go test -tags=integration ./...
terraform -chdir=infra/aws/envs/dev plan
```

Go 1.26 以上、Terraform 1.11 以上を使う。

## 設計の不変条件

変更が必要なら、先に ADR を更新する。

- `internal/core` は AWS SDK に依存しない。クラウド固有の処理は `internal/adapters/*` に置く
- ingest は DynamoDB にのみ書き込む。SQS への投入は Streams 経由の enqueuer が行う
- SQS には ID のみ載せる。ペイロードは DynamoDB から取得する
- DynamoDB のパーティションキーは必ずテナント ID から始める
- テナント ID はオーソライザーの結果だけを信用する。リクエスト本文の値を使わない
- 再送の判断は配信レコードの `attemptCount` で行う。`ApproximateReceiveCount` は使わない
- 再送待ちを SQS の処理中（in-flight）に置かない。`DelaySeconds` か EventBridge Scheduler で入れ直す
- 配信結果を DynamoDB に記録してから、SQS のメッセージを削除する
- 署名は Standard Webhooks に準拠する。`webhook-id` は再送と送信先をまたいで変えない
- 送信先への接続は、必ず SSRF 検査済みの接続処理を通す。リダイレクトは追わない
- 署名シークレット、固定ヘッダー、API キーの平文をログや DynamoDB に残さない

## コーディング規約

- `gofmt` と `golangci-lint` に従う
- 識別子は英語、コメントは日本語
- エラーは `%w` で文脈を付けて返す。無視する場合は理由をコメントに書く
- I/O を伴う関数は `context.Context` を受け取る。goroutine は必ず終了できるようにする
- 外部入力は境界で検証する
- 時刻と乱数は注入し、テストで固定できるようにする

## テスト

- 変更したロジックのテストを追加する
- `core` はユニットテストで検証する
- アダプタと配信フローは DynamoDB Local と ElasticMQ で統合テストする
- 通信失敗の各段階（`docs/architecture.md` の障害時の動作）をテストする

## コミットと PR

- コミットメッセージと PR の本文は日本語で書く
- `*.tfvars`、`.env*`、tfstate をコミットしない。`.terraform.lock.hcl` はコミットする
- Terraform を変更したら `terraform fmt` と `terraform validate` を実行する
- 破壊的な操作（`terraform apply`、`terraform destroy`、push）は依頼がある場合のみ行う
