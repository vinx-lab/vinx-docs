package config

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// LocalRequest 直接连本机服务（不经过代理），失败时返回 (0, nil)。
func LocalRequest(home, path string, timeout time.Duration) (int, []byte) {
	host, port := ServerAddress(home)
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:       nil,
			DialContext: (&net.Dialer{Timeout: timeout}).DialContext,
		},
		// http.client 不跟随重定向，这里也不跟随，才能拿到 301/302。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	url := fmt.Sprintf("http://%s%s", net.JoinHostPort(host, strconv.Itoa(port)), path)
	response, err := client.Get(url)
	if err != nil {
		return 0, nil
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil
	}
	return response.StatusCode, body
}
