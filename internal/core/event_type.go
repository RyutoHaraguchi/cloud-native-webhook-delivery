package core

import (
	"regexp"
	"slices"
)

var eventTypePattern = regexp.MustCompile(`^[a-zA-Z0-9_]+(\.[a-zA-Z0-9_]+)*$`)

// IsValidEventType は eventType がドット区切りの形式に従うかを返す。
func IsValidEventType(eventType string) bool {
	return eventTypePattern.MatchString(eventType)
}

// IsSubscribed は eventType が購読の一覧に含まれるかを返す。
// 完全一致で比べ、大文字と小文字を区別する。
func IsSubscribed(subscribed []string, eventType string) bool {
	return slices.Contains(subscribed, eventType)
}
