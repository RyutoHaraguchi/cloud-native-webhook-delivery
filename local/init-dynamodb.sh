#!/bin/bash
# DynamoDB Local に config と messages のテーブルを作る。
# データはメモリにしかないので、起動のたびに作る。
# down せずに up し直すとテーブルが残っているので、既にあるテーブルは作らない。
set -euo pipefail

endpoint="${DYNAMODB_ENDPOINT:?DYNAMODB_ENDPOINT を指定する}"

ddb() {
  aws dynamodb --endpoint-url "$endpoint" "$@"
}

# 起動直後は接続を拒否されるので、応答するまで待つ
for i in $(seq 1 30); do
  if ddb list-tables >/dev/null 2>&1; then
    break
  fi
  if [ "$i" -eq 30 ]; then
    echo "DynamoDB Local が応答しない: $endpoint" >&2
    exit 1
  fi
  sleep 1
done

# 引数は create-table に渡す。テーブル名は 1 つ目の引数で指定する
create_table() {
  local name="$1"
  shift
  if ddb describe-table --table-name "$name" >/dev/null 2>&1; then
    echo "既にある: $name"
    return
  fi
  ddb create-table --table-name "$name" "$@" >/dev/null
  echo "作成した: $name"
}

create_table config \
  --attribute-definitions AttributeName=PK,AttributeType=S AttributeName=SK,AttributeType=S \
  --key-schema AttributeName=PK,KeyType=HASH AttributeName=SK,KeyType=RANGE \
  --billing-mode PAY_PER_REQUEST

# enqueuer は追加されたアイテムの itemType を見るので、Streams には新しい値を載せる
create_table messages \
  --attribute-definitions AttributeName=PK,AttributeType=S AttributeName=SK,AttributeType=S \
  --key-schema AttributeName=PK,KeyType=HASH AttributeName=SK,KeyType=RANGE \
  --billing-mode PAY_PER_REQUEST \
  --stream-specification StreamEnabled=true,StreamViewType=NEW_IMAGE
