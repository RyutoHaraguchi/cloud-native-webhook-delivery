package aws

import (
	"errors"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestNewMessageRepository_InvalidConfig(t *testing.T) {
	client := &dynamodb.Client{}
	now := func() time.Time { return time.Time{} }
	tests := []struct {
		name string
		cfg  MessageRepositoryConfig
		now  func() time.Time
	}{
		{"empty table", MessageRepositoryConfig{Retention: time.Hour}, now},
		{"zero retention", MessageRepositoryConfig{Table: "messages"}, now},
		{"negative retention", MessageRepositoryConfig{Table: "messages", Retention: -time.Hour}, now},
		{"nil now", MessageRepositoryConfig{Table: "messages", Retention: time.Hour}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewMessageRepository(client, tt.cfg, tt.now); err == nil {
				t.Fatal("err = nil, want error")
			}
		})
	}
}

func TestIsConditionalCheckFailed(t *testing.T) {
	reason := func(code string) types.CancellationReason {
		return types.CancellationReason{Code: awssdk.String(code)}
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"None と ConditionalCheckFailed", &types.TransactionCanceledException{CancellationReasons: []types.CancellationReason{reason("None"), reason("ConditionalCheckFailed")}}, true},
		{"TransactionConflict のみ", &types.TransactionCanceledException{CancellationReasons: []types.CancellationReason{reason("TransactionConflict")}}, false},
		{"理由が nil", &types.TransactionCanceledException{}, false},
		{"TransactionCanceledException 以外", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isConditionalCheckFailed(tt.err); got != tt.want {
				t.Errorf("isConditionalCheckFailed() = %v, want %v", got, tt.want)
			}
		})
	}
}
