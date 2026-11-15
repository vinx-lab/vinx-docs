// Package gdrive 是内置的 Google Drive 适配器（spec 0008 第 9 节）：直接调用 Drive API，只用标准库。
// 授权用 OAuth 设备码流程（auth.begin / auth.poll），令牌存在账号的状态目录里。
package gdrive

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vinx-lab/vinx-docs/internal/share"
	"github.com/vinx-lab/vinx-docs/internal/share/kit"
)

//go:embed adapter.json
var manifest []byte

func init() { share.RegisterBuiltin(manifest, Run) }

// Scopes 是申请的权限：drive.file 只能访问本应用自己创建的文件；email 用来显示是哪个账号。
const Scopes = driveScope + " email openid"

const driveScope = "https://www.googleapis.com/auth/drive.file"

// noDriveMessage 是授权时没勾选 Drive 权限的提示：Google 授权页上 Drive 是单独的勾选框，不勾也能「允许」。
const noDriveMessage = "授权时没有勾选 Google 云端硬盘权限（「查看、修改、创建和删除您通过此应用使用的特定 Google 云端硬盘文件」）。请在后台重新连接，并在 Google 页面上勾选这一项"

const folderMime = "application/vnd.google-apps.folder"

// endpoints 是接口地址；测试时用 VINX_GDRIVE_ENDPOINT 换成本地的假服务。
type endpoints struct{ oauth, api string }

func currentEndpoints() endpoints {
	if base := strings.TrimRight(os.Getenv("VINX_GDRIVE_ENDPOINT"), "/"); base != "" {
		return endpoints{base + "/oauth", base}
	}
	return endpoints{"https://oauth2.googleapis.com", "https://www.googleapis.com"}
}

// Run 是适配器入口（vinx-docs share-adapter run google-drive）。
func Run(stdin io.Reader, stdout, stderr io.Writer) int {
	return kit.Serve(stdin, stdout, map[string]kit.Handler{
		"auth.begin": authBegin,
		"auth.poll":  authPoll,
		"check":      check,
		"publish":    publish,
		"revoke":     revoke,
	})
}

// ---------------------------------------------------------------- 授权

type pendingAuth struct {
	DeviceCode string `json:"deviceCode"`
	ExpiresAt  int64  `json:"expiresAt"`
	Interval   int    `json:"interval"`
}

type token struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	Expiry       int64  `json:"expiry"`
	Email        string `json:"email"`
}

const (
	authFile  = "auth.json"
	tokenFile = "token.json"
)

func credentials(req *kit.Request) (string, string, error) {
	id, secret := req.Provider["clientId"], req.Secrets["clientSecret"]
	if id == "" || secret == "" {
		return "", "", kit.Errorf("invalid_config", "请先在后台填写 Google OAuth 客户端 ID 和密钥")
	}
	return id, secret, nil
}

var httpClient = &http.Client{Timeout: 0}

// postForm 发表单请求，返回状态码和解析后的 JSON。
func postForm(endpoint string, form url.Values) (int, map[string]any, error) {
	resp, err := httpClient.PostForm(endpoint, form)
	if err != nil {
		return 0, nil, kit.Errorf("network", "连不上 Google：%v", err)
	}
	defer resp.Body.Close()
	body := map[string]any{}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
	return resp.StatusCode, body, nil
}

func str(m map[string]any, key string) string  { s, _ := m[key].(string); return s }
func num(m map[string]any, key string) float64 { n, _ := m[key].(float64); return n }

func authBegin(req *kit.Request, out *kit.Out) (map[string]any, error) {
	clientID, _, err := credentials(req)
	if err != nil {
		return nil, err
	}
	status, body, err := postForm(currentEndpoints().oauth+"/device/code", url.Values{"client_id": {clientID}, "scope": {Scopes}})
	if err != nil {
		return nil, err
	}
	if status != 200 || str(body, "device_code") == "" {
		return nil, oauthError(status, body)
	}
	interval := int(num(body, "interval"))
	if interval <= 0 {
		interval = 5
	}
	expires := num(body, "expires_in")
	verification := str(body, "verification_url")
	if verification == "" {
		verification = str(body, "verification_uri")
	}
	pending := pendingAuth{DeviceCode: str(body, "device_code"), ExpiresAt: time.Now().Unix() + int64(expires), Interval: interval}
	if err := kit.WriteState(req.StateDir, authFile, pending); err != nil {
		return nil, err
	}
	return map[string]any{"verificationUrl": verification, "userCode": str(body, "user_code"), "expiresIn": expires, "interval": interval}, nil
}

func authPoll(req *kit.Request, out *kit.Out) (map[string]any, error) {
	clientID, secret, err := credentials(req)
	if err != nil {
		return nil, err
	}
	var pending pendingAuth
	if err := kit.ReadState(req.StateDir, authFile, &pending); err != nil {
		return nil, kit.Errorf("auth_expired", "没有进行中的授权，请重新连接")
	}
	if time.Now().Unix() > pending.ExpiresAt {
		kit.RemoveState(req.StateDir, authFile)
		return nil, kit.Errorf("auth_expired", "验证码已过期，请重新连接")
	}
	status, body, err := postForm(currentEndpoints().oauth+"/token", url.Values{
		"client_id": {clientID}, "client_secret": {secret}, "device_code": {pending.DeviceCode},
		"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
	})
	if err != nil {
		return nil, err
	}
	switch str(body, "error") {
	case "authorization_pending":
		return map[string]any{"status": "pending", "interval": pending.Interval}, nil
	case "slow_down":
		pending.Interval += 5
		_ = kit.WriteState(req.StateDir, authFile, pending)
		return map[string]any{"status": "pending", "interval": pending.Interval}, nil
	case "access_denied":
		kit.RemoveState(req.StateDir, authFile)
		return nil, kit.Errorf("auth_denied", "在 Google 页面上拒绝了授权")
	case "expired_token":
		kit.RemoveState(req.StateDir, authFile)
		return nil, kit.Errorf("auth_expired", "验证码已过期，请重新连接")
	}
	if status != 200 || str(body, "access_token") == "" {
		return nil, oauthError(status, body)
	}
	// Google 的分项授权：用户可以只允许登录信息、不勾选 Drive。这时令牌用不了，不保存，直接提示重新连接。
	if scope := str(body, "scope"); scope != "" && !strings.Contains(scope, driveScope) {
		kit.RemoveState(req.StateDir, authFile)
		return nil, kit.Errorf("auth_denied", "%s", noDriveMessage)
	}
	tok := token{AccessToken: str(body, "access_token"), RefreshToken: str(body, "refresh_token"),
		Expiry: time.Now().Unix() + int64(num(body, "expires_in")), Email: emailFromIDToken(str(body, "id_token"))}
	if tok.RefreshToken == "" {
		return nil, kit.Errorf("internal", "Google 没有返回刷新令牌")
	}
	if err := kit.WriteState(req.StateDir, tokenFile, tok); err != nil {
		return nil, err
	}
	kit.RemoveState(req.StateDir, authFile)
	label := tok.Email
	if label == "" {
		label = "Google 账号"
	}
	return map[string]any{"status": "done", "account": label}, nil
}

// emailFromIDToken 从 ID 令牌（JWT）里取出邮箱；令牌直接来自 Google 的令牌接口（HTTPS），只用来显示，不做签名校验。
func emailFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(payload, &claims)
	return claims.Email
}

func oauthError(status int, body map[string]any) error {
	code, desc := str(body, "error"), str(body, "error_description")
	switch {
	case code == "invalid_client" || code == "unauthorized_client":
		return kit.Errorf("invalid_config", "OAuth 客户端无效（%s）：请检查客户端 ID、密钥和类型（电视和输入受限的设备）", code)
	case code == "invalid_grant":
		return kit.Errorf("auth_required", "授权已失效，请在后台重新连接这个账号")
	case code == "rate_limit_exceeded" || status == 429:
		return kit.Errorf("rate_limited", "请求过于频繁：%s", desc)
	case status >= 500 || status == 0:
		return kit.Errorf("network", "Google 暂时不可用（%d）", status)
	}
	return kit.Errorf("internal", "Google 返回 %d %s %s", status, code, desc)
}

// ---------------------------------------------------------------- Drive 客户端

type client struct {
	req *kit.Request
	tok token
	ep  endpoints
}

func newClient(req *kit.Request) (*client, error) {
	if _, _, err := credentials(req); err != nil {
		return nil, err
	}
	c := &client{req: req, ep: currentEndpoints()}
	if err := kit.ReadState(req.StateDir, tokenFile, &c.tok); err != nil || c.tok.RefreshToken == "" {
		return nil, kit.Errorf("auth_required", "这个账号还没有授权，请在后台连接")
	}
	if time.Now().Unix() > c.tok.Expiry-60 {
		if err := c.refresh(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *client) refresh() error {
	clientID, secret, _ := credentials(c.req)
	status, body, err := postForm(c.ep.oauth+"/token", url.Values{
		"client_id": {clientID}, "client_secret": {secret}, "refresh_token": {c.tok.RefreshToken}, "grant_type": {"refresh_token"},
	})
	if err != nil {
		return err
	}
	if status != 200 || str(body, "access_token") == "" {
		return oauthError(status, body)
	}
	c.tok.AccessToken = str(body, "access_token")
	c.tok.Expiry = time.Now().Unix() + int64(num(body, "expires_in"))
	if rt := str(body, "refresh_token"); rt != "" {
		c.tok.RefreshToken = rt
	}
	return kit.WriteState(c.req.StateDir, tokenFile, c.tok)
}

// do 发一个带令牌的请求；401 时刷新令牌后重试一次。body 用工厂函数，重试时能重新读。
func (c *client) do(method, target string, header http.Header, body func() (io.Reader, int64)) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		var size int64 = -1
		if body != nil {
			reader, size = body()
		}
		req, err := http.NewRequest(method, target, reader)
		if err != nil {
			return nil, err
		}
		if size >= 0 {
			req.ContentLength = size
		}
		for k, v := range header {
			req.Header[k] = v
		}
		req.Header.Set("Authorization", "Bearer "+c.tok.AccessToken)
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, kit.Errorf("network", "连不上 Google Drive：%v", err)
		}
		if resp.StatusCode == 401 && attempt == 0 {
			resp.Body.Close()
			if err := c.refresh(); err != nil {
				return nil, err
			}
			continue
		}
		return resp, nil
	}
}

// apiError 把 Drive 的错误响应映射成协议错误码。
func apiError(resp *http.Response) error {
	var body struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
	reason := ""
	if len(body.Error.Errors) > 0 {
		reason = body.Error.Errors[0].Reason
	}
	msg := fmt.Sprintf("Drive 返回 %d %s %s", resp.StatusCode, reason, body.Error.Message)
	switch {
	case resp.StatusCode == 401:
		return kit.Errorf("auth_required", "授权已失效，请在后台重新连接这个账号")
	case resp.StatusCode == 403 && (reason == "insufficientPermissions" || strings.Contains(body.Error.Message, "insufficient authentication scopes")):
		return kit.Errorf("auth_required", "%s", noDriveMessage)
	case reason == "storageQuotaExceeded" || reason == "quotaExceeded":
		return kit.Errorf("quota_exceeded", "%s", msg)
	case reason == "rateLimitExceeded" || reason == "userRateLimitExceeded" || resp.StatusCode == 429:
		return kit.Errorf("rate_limited", "%s", msg)
	case resp.StatusCode == 403:
		return kit.Errorf("permission_denied", "%s", msg)
	case resp.StatusCode == 404:
		return kit.Errorf("not_found", "%s", msg)
	case resp.StatusCode >= 500:
		return kit.Errorf("network", "%s", msg)
	}
	return kit.Errorf("internal", "%s", msg)
}

// jsonCall 发 JSON 请求，成功时把响应解析到 out。
func (c *client) jsonCall(method, target string, payload any, out any) error {
	var factory func() (io.Reader, int64)
	header := http.Header{}
	if payload != nil {
		data, _ := json.Marshal(payload)
		factory = func() (io.Reader, int64) { return bytes.NewReader(data), int64(len(data)) }
		header.Set("Content-Type", "application/json; charset=UTF-8")
	}
	resp, err := c.do(method, target, header, factory)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
	}
	return nil
}

func quote(s string) string { return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'" }

type fileList struct {
	Files []struct {
		ID string `json:"id"`
	} `json:"files"`
}

func (c *client) findOne(q string) (string, error) {
	var list fileList
	target := c.ep.api + "/drive/v3/files?" + url.Values{"q": {q}, "fields": {"files(id)"}, "pageSize": {"10"}, "spaces": {"drive"}}.Encode()
	if err := c.jsonCall("GET", target, nil, &list); err != nil {
		return "", err
	}
	if len(list.Files) == 0 {
		return "", nil
	}
	return list.Files[0].ID, nil
}

// ensureFolder 从根目录开始逐级查找或创建文件夹，返回最后一级的 id；dir 为空时返回 root。
func (c *client) ensureFolder(dir string) (string, error) {
	parent := "root"
	for _, name := range strings.Split(dir, "/") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		id, err := c.findOne(fmt.Sprintf("name = %s and mimeType = '%s' and %s in parents and trashed = false", quote(name), folderMime, quote(parent)))
		if err != nil {
			return "", err
		}
		if id == "" {
			var created struct {
				ID string `json:"id"`
			}
			err := c.jsonCall("POST", c.ep.api+"/drive/v3/files?fields=id", map[string]any{"name": name, "mimeType": folderMime, "parents": []string{parent}}, &created)
			if err != nil {
				return "", err
			}
			id = created.ID
		}
		parent = id
	}
	return parent, nil
}

type fileInfo struct {
	ID          string `json:"id"`
	WebViewLink string `json:"webViewLink"`
	Trashed     bool   `json:"trashed"`
}

// stat 返回文件信息；不存在时返回 nil。
func (c *client) stat(id string) (*fileInfo, error) {
	var info fileInfo
	err := c.jsonCall("GET", c.ep.api+"/drive/v3/files/"+url.PathEscape(id)+"?fields=id,webViewLink,trashed", nil, &info)
	if e, ok := err.(*kit.Error); ok && e.Code == "not_found" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// upload 用可续传上传新建（id 为空）或覆盖文件内容，返回文件 id。
func (c *client) upload(id string, metadata map[string]any, path string, size int64, out *kit.Out) (string, error) {
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	header := http.Header{}
	header.Set("Content-Type", "application/json; charset=UTF-8")
	header.Set("X-Upload-Content-Type", contentType)
	header.Set("X-Upload-Content-Length", fmt.Sprint(size))
	method, target := "POST", c.ep.api+"/upload/drive/v3/files?uploadType=resumable&fields=id"
	if id != "" {
		method, target = "PATCH", c.ep.api+"/upload/drive/v3/files/"+url.PathEscape(id)+"?uploadType=resumable&fields=id"
	}
	meta, _ := json.Marshal(metadata)
	resp, err := c.do(method, target, header, func() (io.Reader, int64) { return bytes.NewReader(meta), int64(len(meta)) })
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", apiError(resp)
	}
	location := resp.Header.Get("Location")
	if location == "" {
		return "", kit.Errorf("internal", "Drive 没有返回上传地址")
	}
	out.Log("上传 %d 字节", size)
	putHeader := http.Header{}
	putHeader.Set("Content-Type", contentType)
	resp, err = c.do("PUT", location, putHeader, func() (io.Reader, int64) {
		file, err := os.Open(path)
		if err != nil {
			return bytes.NewReader(nil), 0
		}
		return file, size
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", apiError(resp)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&created); err != nil || created.ID == "" {
		return "", kit.Errorf("internal", "Drive 没有返回文件 id")
	}
	out.Progress(size, size)
	return created.ID, nil
}

// ---------------------------------------------------------------- 方法

func check(req *kit.Request, out *kit.Out) (map[string]any, error) {
	c, err := newClient(req)
	if err != nil {
		return nil, err
	}
	dir := req.Target.Config["dir"]
	// 目录为空时 ensureFolder 不请求 Drive；先调一次 about，确认令牌真的能用 Drive（例如授权时勾选了 Drive 权限）。
	if err := c.jsonCall("GET", c.ep.api+"/drive/v3/about?fields=user", nil, nil); err != nil {
		return nil, err
	}
	if _, err := c.ensureFolder(dir); err != nil {
		return nil, err
	}
	account := c.tok.Email
	if dir != "" {
		account += " · " + dir
	}
	return map[string]any{"account": account}, nil
}

func publish(req *kit.Request, out *kit.Out) (map[string]any, error) {
	var p kit.PublishParams
	if err := json.Unmarshal(req.Params, &p); err != nil || p.File == "" {
		return nil, kit.Errorf("internal", "publish 参数无效")
	}
	// 大小以实际文件为准（不信任参数里的值）。
	info, err := os.Stat(p.File)
	if err != nil {
		return nil, kit.Errorf("internal", "读不到要上传的文件：%v", err)
	}
	p.Size = info.Size()
	c, err := newClient(req)
	if err != nil {
		return nil, err
	}
	// 找已有的文件：先用上次的 id，再按幂等键找（重试或结果未知后再发，不产生重复文件）。
	id := ""
	if p.Previous != nil && p.Previous.RemoteID != "" {
		if info, err := c.stat(p.Previous.RemoteID); err != nil {
			return nil, err
		} else if info != nil && !info.Trashed {
			id = info.ID
		}
	}
	if id == "" && req.Key != "" {
		if id, err = c.findOne(fmt.Sprintf("appProperties has { key='vinxKey' and value=%s } and trashed = false", quote(req.Key))); err != nil {
			return nil, err
		}
	}
	if id == "" {
		folder, err := c.ensureFolder(req.Target.Config["dir"])
		if err != nil {
			return nil, err
		}
		meta := map[string]any{"name": p.Name, "parents": []string{folder}, "appProperties": map[string]string{"vinxKey": req.Key}}
		if id, err = c.upload("", meta, p.File, p.Size, out); err != nil {
			return nil, err
		}
	} else if _, err := c.upload(id, map[string]any{}, p.File, p.Size, out); err != nil {
		return nil, err
	}
	if req.Target.Public {
		if err := c.jsonCall("POST", c.ep.api+"/drive/v3/files/"+url.PathEscape(id)+"/permissions?fields=id",
			map[string]string{"type": "anyone", "role": "reader"}, nil); err != nil {
			return nil, err
		}
	}
	file, err := c.stat(id)
	if err != nil {
		return nil, err
	}
	link := "https://drive.google.com/file/d/" + id + "/view"
	if file != nil && file.WebViewLink != "" {
		link = file.WebViewLink
	}
	return map[string]any{"url": link, "remoteId": id, "public": req.Target.Public}, nil
}

func revoke(req *kit.Request, out *kit.Out) (map[string]any, error) {
	var p kit.RevokeParams
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Previous.RemoteID == "" {
		return nil, kit.Errorf("internal", "revoke 参数无效")
	}
	c, err := newClient(req)
	if err != nil {
		return nil, err
	}
	info, err := c.stat(p.Previous.RemoteID)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return map[string]any{"alreadyGone": true}, nil
	}
	// 先取消「知道链接的任何人」：回收站里的文件，公开链接仍然能打开和下载（实测），只移到回收站不够。
	// 非公开的文件没有这个权限，返回 404，忽略。
	err = c.jsonCall("DELETE", c.ep.api+"/drive/v3/files/"+url.PathEscape(info.ID)+"/permissions/anyoneWithLink", nil, nil)
	var kerr *kit.Error
	if err != nil && !(errors.As(err, &kerr) && kerr.Code == "not_found") {
		return nil, err
	}
	if info.Trashed {
		return map[string]any{"alreadyGone": true}, nil
	}
	// 再移到回收站：用户还能从回收站恢复（恢复后只有本人能看）。
	if err := c.jsonCall("PATCH", c.ep.api+"/drive/v3/files/"+url.PathEscape(info.ID), map[string]bool{"trashed": true}, nil); err != nil {
		return nil, err
	}
	return map[string]any{"alreadyGone": false}, nil
}
