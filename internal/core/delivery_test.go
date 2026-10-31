package core

import "testing"

func TestCanTransition(t *testing.T) {
	all := []DeliveryStatus{
		StatusPending,
		StatusRetrying,
		StatusDelivered,
		StatusFailed,
		StatusCanceled,
	}

	allowed := map[[2]DeliveryStatus]bool{
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

	for _, from := range all {
		for _, to := range all {
			want := allowed[[2]DeliveryStatus{from, to}]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestCanTransitionUnknownStatus(t *testing.T) {
	if CanTransition("unknown", StatusPending) {
		t.Error("未知の状態からの遷移を許可してはいけない")
	}
	if CanTransition(StatusPending, "unknown") {
		t.Error("未知の状態への遷移を許可してはいけない")
	}
}
