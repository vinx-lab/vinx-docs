package server

import (
	"io"
	"log"
)

// nullLogger 丢弃 net/http 的内部日志：服务不写访问日志。
func nullLogger() *log.Logger { return log.New(io.Discard, "", 0) }
