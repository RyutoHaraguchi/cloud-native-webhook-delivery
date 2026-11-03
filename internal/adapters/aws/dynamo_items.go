package aws

import (
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// messages テーブルと config テーブルの鍵。パーティションキーは必ずテナント ID から始める
const (
	// messageSK はメッセージ本体の項目のソートキー
	messageSK = "META"
	// endpointSKPrefix はエンドポイントの項目のソートキーの接頭辞。テナント内の一覧に使う
	endpointSKPrefix = "ENDPOINT#"
)

// messagePK はメッセージと、それにぶら下がる配信・試行の項目のパーティションキーを返す
func messagePK(tenantID, messageID string) string {
	return "TENANT#" + tenantID + "#MSG#" + messageID
}

// deliverySK は送信先ごとの配信レコードのソートキーを返す
func deliverySK(endpointID string) string {
	return "DELIVERY#" + endpointID
}

// attemptSK は試行の項目のソートキーを返す。番号を 6 桁で 0 埋めし、文字列の順を試行の順にそろえる
func attemptSK(endpointID string, seq int) string {
	return fmt.Sprintf("ATTEMPT#%s#%06d", endpointID, seq)
}

// endpointPK はテナントの設定（エンドポイントなど）のパーティションキーを返す
func endpointPK(tenantID string) string {
	return "TENANT#" + tenantID
}

// endpointSK はエンドポイントの項目のソートキーを返す
func endpointSK(endpointID string) string {
	return endpointSKPrefix + endpointID
}

// requireAttributes は、指定した属性がすべてあり、どれも NULL 型でないことを確かめる。
// 保存値は信用せず、読み出した項目を型に変換する前に呼ぶ
func requireAttributes(item map[string]types.AttributeValue, names ...string) error {
	var errs []error
	for _, name := range names {
		v, ok := item[name]
		if !ok {
			errs = append(errs, fmt.Errorf("attribute %q is missing", name))
			continue
		}
		if _, isNull := v.(*types.AttributeValueMemberNULL); isNull {
			errs = append(errs, fmt.Errorf("attribute %q is NULL", name))
		}
	}
	return errors.Join(errs...)
}

// timeLayout は保存する時刻の書式。小数部を 9 桁に固定し、文字列の大小を時刻の前後と一致させる
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// formatTime は時刻を UTC に直し、固定長の RFC 3339 の文字列にする
func formatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// parseTime は formatTime で保存した文字列を時刻に戻す
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t, nil
}
