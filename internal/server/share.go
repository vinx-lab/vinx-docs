package server

import (
	"encoding/json"
	"errors"

	"github.com/vinx-lab/vinx-docs/internal/api"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/share"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// 发布到外部服务的接口（spec 0008 第 7 节）。目标的登记只能在本机命令行做，这里没有写目标的接口。
var shareGets = map[string]bool{"/api/share-targets": true, "/api/shares": true, "/api/shares/job": true, "/api/share-admin": true}

// 页面上能管理的只有内置适配器的服务商设置、账号和目标；命令、清单路径、passEnv 不在可接受的字段里（ADR 0006）。
var sharePosts = map[string]api.RouteSpec{
	"/api/shares":                 {Required: []string{"page", "target"}, Limit: 4096},
	"/api/shares/revoke":          {Required: []string{"share"}, Limit: 4096},
	"/api/shares/forget":          {Required: []string{"share"}, Limit: 4096},
	"/api/share-providers/set":    {Required: []string{"adapter"}, Optional: []string{"config", "secrets"}, Limit: 65536},
	"/api/share-accounts/connect": {Optional: []string{"adapter", "account"}, Limit: 4096},
	"/api/share-accounts/poll":    {Required: []string{"account"}, Limit: 4096},
	"/api/share-accounts/remove":  {Required: []string{"account"}, Limit: 4096},
	"/api/share-targets/save":     {Required: []string{"label", "adapter"}, Optional: []string{"id", "account", "config", "public", "sources", "naming"}, Limit: 65536},
	"/api/share-targets/remove":   {Required: []string{"id"}, Optional: []string{"force"}, Limit: 4096},
	"/api/share-targets/test":     {Required: []string{"id"}, Limit: 4096},
}

// decodeBody 把请求体转成结构体；字段类型不对时返回 false 并写好 400。
func (q *request) decodeBody(body *ojson.Object, out any) bool {
	if err := json.Unmarshal([]byte(ojson.Dumps(body, -1)), out); err != nil {
		q.reply(400, errorBody("请求格式无效："+err.Error()))
		return false
	}
	return true
}

// jsonValue 把结构体转成 reply 能输出的值（ojson 按插入顺序输出对象）。
func jsonValue(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		abort()
	}
	value, err := ojson.Decode(data)
	if err != nil {
		abort()
	}
	return value
}

func (q *request) shareError(err error) {
	var userErr *share.UserError
	if errors.As(err, &userErr) {
		q.reply(userErr.Status, errorBody(userErr.Message))
		return
	}
	q.reply(500, errorBody("发布记录或目标配置读写失败："+err.Error()))
}

func (q *request) serveShareGet(path string) {
	params := textutil.ParseQS(q.query)
	first := func(key string) string {
		if values := params[key]; len(values) > 0 {
			return values[0]
		}
		return ""
	}
	switch path {
	case "/api/share-targets":
		// 经反向代理的外部访问不能发布：不列出目标，页面上就不显示「发布到」。
		if q.external {
			q.reply(200, ojson.NewObject("targets", []any{}, "canWrite", false))
			return
		}
		targets, err := q.s.Share.Targets(first("page"))
		if err != nil {
			q.shareError(err)
			return
		}
		source, err := q.s.Share.Source(first("page"))
		if err != nil {
			q.shareError(err)
			return
		}
		q.reply(200, ojson.NewObject("targets", jsonValue(targets), "source", jsonValue(source), "canWrite", true))
	case "/api/shares":
		shares, err := q.s.Share.Shares(first("page"))
		if err != nil {
			q.shareError(err)
			return
		}
		q.reply(200, ojson.NewObject("shares", jsonValue(shares), "canWrite", !q.external))
	case "/api/share-admin":
		if q.external {
			q.reply(403, errorBody("只有内网可以管理外部发布"))
			return
		}
		data, err := q.s.Share.Admin()
		if err != nil {
			q.shareError(err)
			return
		}
		q.reply(200, jsonValue(data))
	default:
		job := q.s.Share.JobByID(first("id"))
		if job == nil {
			q.reply(404, errorBody("任务不存在（服务重启后任务会清空，请刷新查看记录）"))
			return
		}
		q.reply(200, jsonValue(job))
	}
}

func (q *request) serveSharePost(body *ojson.Object) {
	if q.external {
		q.reply(403, errorBody("只有内网可以发布到外部服务、撤回或管理目标"))
		return
	}
	str := func(key string) string { value, _ := body.Value(key).(string); return value }
	var job *share.Job
	var err error
	switch q.full {
	case "/api/share-providers/set":
		var in struct {
			Adapter string            `json:"adapter"`
			Config  map[string]string `json:"config"`
			Secrets map[string]string `json:"secrets"`
		}
		if !q.decodeBody(body, &in) {
			return
		}
		if err := q.s.Share.SetProvider(in.Adapter, in.Config, in.Secrets); err != nil {
			q.shareError(err)
			return
		}
		q.reply(200, ojson.NewObject("ok", true))
		return
	case "/api/share-accounts/connect":
		start, err := q.s.Share.ConnectAccount(str("adapter"), str("account"))
		if err != nil {
			q.shareError(err)
			return
		}
		q.reply(200, jsonValue(start))
		return
	case "/api/share-accounts/poll":
		poll, err := q.s.Share.PollAccount(str("account"))
		if err != nil {
			q.shareError(err)
			return
		}
		q.reply(200, jsonValue(poll))
		return
	case "/api/share-accounts/remove":
		if err := q.s.Share.RemoveAccount(str("account")); err != nil {
			q.shareError(err)
			return
		}
		q.reply(200, ojson.NewObject("ok", true))
		return
	case "/api/share-targets/save":
		var in share.TargetInput
		if !q.decodeBody(body, &in) {
			return
		}
		id, account, err := q.s.Share.SaveTarget(in, false)
		if err != nil {
			q.shareError(err)
			return
		}
		q.reply(200, ojson.NewObject("id", id, "account", account))
		return
	case "/api/share-targets/remove":
		if err := q.s.Share.RemoveTarget(str("id"), body.Value("force") == true); err != nil {
			q.shareError(err)
			return
		}
		q.reply(200, ojson.NewObject("ok", true))
		return
	case "/api/share-targets/test":
		account, err := q.s.Share.TestTarget(str("id"))
		if err != nil {
			q.shareError(err)
			return
		}
		q.reply(200, ojson.NewObject("account", account))
		return
	case "/api/shares":
		job, err = q.s.Share.Publish(str("page"), str("target"))
	case "/api/shares/revoke":
		job, err = q.s.Share.Revoke(str("share"))
	default:
		if err = q.s.Share.Forget(str("share")); err == nil {
			q.reply(200, ojson.NewObject("ok", true))
			return
		}
	}
	if err != nil {
		q.shareError(err)
		return
	}
	q.reply(202, ojson.NewObject("job", jsonValue(job)))
}

// shareTargetCount 返回页面能用的发布目标数（单独打开的 HTML 页面据此决定是否显示「发布到…」）。
func (s *Server) shareTargetCount(page string) int {
	targets, err := s.Share.Targets(page)
	if err != nil {
		return 0
	}
	return len(targets)
}

// liveShares 返回页面还占用远端位置的发布记录数（页面列表提示「还有 N 处外部发布」）。
func (s *Server) liveShares(page string) int {
	records, err := share.LoadRecords(s.Root)
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range records {
		if r.Source.Kind == "page" && r.Source.ID == page && r.Live() {
			n++
		}
	}
	return n
}
