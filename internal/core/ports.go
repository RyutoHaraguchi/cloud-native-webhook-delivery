package core

import (
	"context"
	"time"
)

// Attempt は 1 回の送信の記録を表す。StatusCode は応答がないときは 0 にする。
type Attempt struct {
	AttemptedAt time.Time
	Outcome     AttemptOutcome
	StatusCode  int
	Duration    time.Duration
}

// QueuedDelivery はキューから受け取った 1 件の配信を表す。
// Receipt は Ack に渡す受領の識別子。本文を読めなかったときは Err にその理由が入り、Key は使えない。
type QueuedDelivery struct {
	Key     DeliveryKey
	Receipt string
	Err     error
}

// MessageRepository はメッセージと配信を保存する。
type MessageRepository interface {
	// CreateMessage はメッセージを保存する。同じ ID のメッセージが既にあれば ErrConflict を返す。
	CreateMessage(ctx context.Context, m Message) error
	// GetMessage はメッセージを取得する。なければ ErrNotFound、不正な ID は ErrInvalidKey を返す。
	GetMessage(ctx context.Context, tenantID, messageID string) (Message, error)
	// CreateDelivery は作成直後の配信を保存する。既にあれば何もせず nil を返す。
	CreateDelivery(ctx context.Context, key DeliveryKey) error
	// GetDelivery は配信を取得する。なければ ErrNotFound を返す。
	GetDelivery(ctx context.Context, key DeliveryKey) (Delivery, error)
	// SaveDelivery は prev の Status と AttemptCount が保存値と一致するときだけ next を書く。
	// 一致しない、またはアイテムがなければ ErrConflict を返す。
	SaveDelivery(ctx context.Context, key DeliveryKey, prev, next Delivery) error
	// SaveAttempt は SaveDelivery と同じ条件で next を書き、同時に試行の記録を書く。
	// 配信と試行を一方だけ書くことはない。試行の通し番号は next.AttemptSeq() を使う。
	SaveAttempt(ctx context.Context, key DeliveryKey, prev, next Delivery, attempt Attempt) error
}

// EndpointRepository はテナントのエンドポイントを読み書きする。
type EndpointRepository interface {
	// Get はエンドポイントを取得する。なければ ErrNotFound、不正な ID は ErrInvalidKey を返す。
	Get(ctx context.Context, tenantID, endpointID string) (Endpoint, error)
	// ListActive はテナントの active なエンドポイントを返す。不正な ID は ErrInvalidKey を返す。
	// 壊れた送信先を除くと配信を取り返せないので、1 件でも壊れていればエラーにする。
	ListActive(ctx context.Context, tenantID string) ([]Endpoint, error)
	// Disable はエンドポイントを disabled にする。削除済みなら何もせず nil を返す。
	// 不正な ID は ErrInvalidKey を返す。
	Disable(ctx context.Context, tenantID, endpointID string) error
}

// SecretCipher はシークレットと固定ヘッダーを、SecretContext を暗号化コンテキストにして暗号化と復号をする。
type SecretCipher interface {
	// Encrypt は plaintext を sc に結び付けて暗号化する。sc が不正なら ErrInvalidKey を返す。
	Encrypt(ctx context.Context, sc SecretContext, plaintext []byte) ([]byte, error)
	// Decrypt は ciphertext を復号する。暗号化と同じ sc でなければ ErrUndecryptable、
	// sc が不正なら ErrInvalidKey を返す。それ以外のエラーは一時的なもので、やり直せば成功しうる。
	Decrypt(ctx context.Context, sc SecretContext, ciphertext []byte) ([]byte, error)
}

// DeliveryPublisher は配信をキューに入れる。
type DeliveryPublisher interface {
	// Send は delay だけ遅らせて配信をキューに入れる。delay が 15 分を超えれば ErrDelayTooLong を返す。
	Send(ctx context.Context, key DeliveryKey, delay time.Duration) error
	// SendToDLQ は配信を DLQ に入れる。
	SendToDLQ(ctx context.Context, key DeliveryKey) error
}

// DeliveryConsumer はキューから配信を受け取る。
type DeliveryConsumer interface {
	// Receive は最大 max 件の配信を受け取る。本文を読めないものは Err を入れて返す。
	Receive(ctx context.Context, max int) ([]QueuedDelivery, error)
	// Ack は受け取った配信をキューから消す。receipt には QueuedDelivery.Receipt を渡す。
	Ack(ctx context.Context, receipt string) error
}

// WebhookRequest は送信先へ 1 回送るリクエストを表す。
type WebhookRequest struct {
	URL     string
	Headers map[string]string
	Body    []byte
}

// WebhookResponse は送信先からの応答を表す。
type WebhookResponse struct {
	StatusCode int
	RetryAfter string // Retry-After ヘッダーの値。なければ空
}

// WebhookSender は SSRF 検査を通して 1 回送る。応答があればステータスに関係なく nil エラーで返す。
// 検査で止めたら ErrSSRFBlocked、タイムアウト、DNS の失敗、接続失敗はそれ以外のエラーを返す。
type WebhookSender interface {
	Send(ctx context.Context, req WebhookRequest) (WebhookResponse, error)
}
