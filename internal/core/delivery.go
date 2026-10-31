package core

// DeliveryStatus は配信の状態を表す。値は永続化されるので変更しない。
type DeliveryStatus string

const (
	DeliveryStatusPending   DeliveryStatus = "pending"
	DeliveryStatusRetrying  DeliveryStatus = "retrying"
	DeliveryStatusDelivered DeliveryStatus = "delivered"
	DeliveryStatusFailed    DeliveryStatus = "failed"
	DeliveryStatusCanceled  DeliveryStatus = "canceled"
)

var allowedTransitions = map[[2]DeliveryStatus]bool{
	{DeliveryStatusPending, DeliveryStatusDelivered}:  true,
	{DeliveryStatusPending, DeliveryStatusRetrying}:   true,
	{DeliveryStatusPending, DeliveryStatusFailed}:     true,
	{DeliveryStatusPending, DeliveryStatusCanceled}:   true,
	{DeliveryStatusRetrying, DeliveryStatusDelivered}: true,
	{DeliveryStatusRetrying, DeliveryStatusRetrying}:  true,
	{DeliveryStatusRetrying, DeliveryStatusFailed}:    true,
	{DeliveryStatusRetrying, DeliveryStatusCanceled}:  true,
	{DeliveryStatusFailed, DeliveryStatusPending}:     true,
}

// CanTransition は from から to へ遷移できるかを返す。
// 状態の組み合わせだけを判定し、失敗理由や送信回数は見ない。
func CanTransition(from DeliveryStatus, to DeliveryStatus) bool {
	return allowedTransitions[[2]DeliveryStatus{from, to}]
}
