package core

type DeliveryStatus string

const (
	StatusPending   DeliveryStatus = "pending"
	StatusRetrying  DeliveryStatus = "retrying"
	StatusDelivered DeliveryStatus = "delivered"
	StatusFailed    DeliveryStatus = "failed"
	StatusCanceled  DeliveryStatus = "canceled"
)

var allowedTrasitions = map[[2]DeliveryStatus]bool{
	{StatusPending, StatusDelivered}:  true,
	{StatusPending, StatusRetrying}:   true,
	{StatusPending, StatusFailed}:     true,
	{StatusPending, StatusCanceled}:   true,
	{StatusRetrying, StatusDelivered}: true,
	{StatusRetrying, StatusRetrying}:  true,
	{StatusRetrying, StatusFailed}:    true,
	{StatusRetrying, StatusCanceled}:  true,
	{StatusFailed, StatusPending}:     true,
}

func CanTransition(from DeliveryStatus, to DeliveryStatus) bool {
	return allowedTrasitions[[2]DeliveryStatus{from, to}]
}
