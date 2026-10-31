package core

import "testing"

func TestCanTransition(t *testing.T) {
	all := []DeliveryStatus{
		DeliveryStatusPending,
		DeliveryStatusRetrying,
		DeliveryStatusDelivered,
		DeliveryStatusFailed,
		DeliveryStatusCanceled,
	}

	allowed := map[[2]DeliveryStatus]bool{
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
	if CanTransition("unknown", DeliveryStatusPending) {
		t.Error("未知の状態からの遷移を許可してはいけない")
	}
	if CanTransition(DeliveryStatusPending, "unknown") {
		t.Error("未知の状態への遷移を許可してはいけない")
	}
}

func TestDeliveryStatusValues(t *testing.T) {
	statuses := map[DeliveryStatus]string{
		DeliveryStatusPending:   "pending",
		DeliveryStatusRetrying:  "retrying",
		DeliveryStatusDelivered: "delivered",
		DeliveryStatusFailed:    "failed",
		DeliveryStatusCanceled:  "canceled",
	}

	for status, want := range statuses {
		if string(status) != want {
			t.Errorf("status = %q, want %q", status, want)
		}
	}
}
