// Package server HTTP API 层。
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"courtbook/internal/service"
	"courtbook/internal/store"
	"courtbook/internal/worker"
)

type Server struct {
	svc              *service.Service
	worker           *worker.Worker
	deviceKey        string
	testClockEnabled bool
	route            *router
}

func New(svc *service.Service, w *worker.Worker) *Server {
	s := &Server{
		svc:              svc,
		worker:           w,
		deviceKey:        getenv("DEVICE_KEY", "device-key-123"),
		testClockEnabled: os.Getenv("ENABLE_TEST_CLOCK") == "true",
		route:            newRouter(),
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", s.route)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.Handle("/swagger/", s.swaggerHandler())
	mux.HandleFunc("/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		_, _ = w.Write([]byte(openAPIYAML))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(indexPage))
			return
		}
		http.NotFound(w, r)
	})
	return logRequests(mux)
}

// ---------- 上下文与鉴权 ----------

type ctxKey string

const staffKey ctxKey = "staff"
const memberKey ctxKey = "member"

func (s *Server) requireStaff(roles ...string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			st, err := s.svc.AuthenticateStaff(r.Context(), bearer(r))
			if err != nil {
				writeErr(w, 401, err.Error())
				return
			}
			if len(roles) > 0 {
				ok := false
				for _, ro := range roles {
					if st.Role == ro {
						ok = true
					}
				}
				if !ok {
					writeErr(w, 403, "角色无权操作（需要: "+strings.Join(roles, "/")+"）")
					return
				}
			}
			ctx := context.WithValue(r.Context(), staffKey, st)
			next(w, r.WithContext(ctx))
		}
	}
}

func (s *Server) requireMember(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, err := s.svc.AuthenticateMember(r.Context(), bearer(r))
		if err != nil {
			writeErr(w, 401, err.Error())
			return
		}
		ctx := context.WithValue(r.Context(), memberKey, m)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) requireDevice(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Device-Key") != s.deviceKey {
			writeErr(w, 401, "硬件回调密钥无效（X-Device-Key）")
			return
		}
		next(w, r)
	}
}

func mustStaff(r *http.Request) *store.Staff {
	return r.Context().Value(staffKey).(*store.Staff)
}

func mustMember(r *http.Request) *store.Member {
	return r.Context().Value(memberKey).(*store.Member)
}

// ---------- 辅助 ----------

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	return dec.Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

const indexPage = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<title>城市体育馆羽毛球预约服务</title>
<style>body{font-family:system-ui,sans-serif;max-width:720px;margin:60px auto;line-height:1.7;padding:0 16px}
a.btn{display:inline-block;margin:8px 12px 8px 0;padding:10px 18px;background:#1668dc;color:#fff;border-radius:8px;text-decoration:none}</style>
</head><body><h1>🏸 城市体育馆羽毛球场地预约服务</h1>
<p>预约 · 闸机 · 灯控 · 计费 · 爽约判定 · 争议处理 一体化后端。</p>
<a class="btn" href="/swagger/">Swagger UI 接口文档</a>
<a class="btn" href="/openapi.yaml">OpenAPI 描述文件</a>
<h3>测试账号</h3>
<ul><li>运营主管 admin / admin123</li><li>运营员 operator / oper123</li>
<li>前台 front / front123</li><li>客服 cs / cs123</li>
<li>会员登录：手机号 13800000001 等，验证码固定 123456</li>
<li>硬件回调头：X-Device-Key: device-key-123</li></ul></body></html>`
