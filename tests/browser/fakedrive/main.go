// fakedrive 是浏览器测试用的假 Google 服务（设备码授权和 Drive 接口），只监听 127.0.0.1。
// 启动后在 stdout 打印一行地址；GET /__files 返回假网盘里的文件列表；POST /__revoke-refresh 让所有令牌失效（模拟授权过期）。
// 用法：go run ./tests/browser/fakedrive
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/vinx-lab/vinx-docs/internal/share/adapters/gdrive/gdrivetest"
)

func main() {
	fake := gdrivetest.NewHandler()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/__files", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fake.Files())
	})
	mux.HandleFunc("/__revoke-refresh", func(w http.ResponseWriter, r *http.Request) {
		fake.RevokeRefresh()
		w.WriteHeader(204)
	})
	mux.Handle("/", fake)
	fmt.Printf("http://%s\n", listener.Addr())
	_ = http.Serve(listener, mux)
}
