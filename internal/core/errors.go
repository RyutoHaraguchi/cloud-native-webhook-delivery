package core

import "errors"

// アダプタが返す番兵エラー。呼び出し側は errors.Is で判定する。
var (
	// ErrInvalidKey は ID や鍵が空、または区切り文字 # を含むなど、キーに使えないことを表す。
	ErrInvalidKey = errors.New("invalid key")
	// ErrNotFound は指定したアイテムが存在しないことを表す。
	ErrNotFound = errors.New("not found")
	// ErrConflict は条件付き書き込みの条件が満たされず、書き込まなかったことを表す。
	ErrConflict = errors.New("conflict")
	// ErrDelayTooLong はキューに入れるときの遅延が、キューで指定できる上限を超えていることを表す。
	ErrDelayTooLong = errors.New("delay too long")
)
