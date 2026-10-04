package server

import (
	"context"
	"net/http"
	"strings"
)

type pathParamsKey struct{}

// router 极简方法+路径模式路由，支持 {param} 段（兼容 Go 1.19，无 SetPathValue）
type router struct {
	routes []rt
}

type rt struct {
	method string
	pieces []string
	h      http.HandlerFunc
}

func newRouter() *router { return &router{} }

func (r *router) Handle(method, pattern string, h http.HandlerFunc) {
	r.routes = append(r.routes, rt{method, strings.Split(strings.Trim(pattern, "/"), "/"), h})
}

func (r *router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := strings.Trim(req.URL.Path, "/")
	got := strings.Split(path, "/")
	for _, e := range r.routes {
		if e.method != req.Method || len(e.pieces) != len(got) {
			continue
		}
		match := true
		params := map[string]string{}
		for i, p := range e.pieces {
			if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
				params[strings.TrimSuffix(strings.TrimPrefix(p, "{"), "}")] = got[i]
				continue
			}
			if p != got[i] {
				match = false
				break
			}
		}
		if match {
			ctx := context.WithValue(req.Context(), pathParamsKey{}, params)
			e.h(w, req.WithContext(ctx))
			return
		}
	}
	writeErr(w, 404, "接口不存在: "+req.Method+" "+req.URL.Path)
}

func pathValue(r *http.Request, key string) string {
	if m, ok := r.Context().Value(pathParamsKey{}).(map[string]string); ok {
		return m[key]
	}
	return ""
}
