package server

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
)

// TestOpenAPIValid 嵌入的 OpenAPI 文档必须能被解析
func TestOpenAPIValid(t *testing.T) {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(openAPIYAML), &doc); err != nil {
		t.Fatalf("openapi.yaml 解析失败: %v", err)
	}
	if v, _ := doc["openapi"].(string); !strings.HasPrefix(v, "3.") {
		t.Fatalf("openapi 版本异常: %v", doc["openapi"])
	}
	paths, _ := doc["paths"].(map[any]any)
	if len(paths) == 0 {
		t.Fatal("paths 为空")
	}
	// 所有 $ref 目标必须存在
	resolve := func(ref string) bool {
		var node any = doc
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			m, ok := node.(map[any]any)
			if !ok {
				m2, ok2 := node.(map[string]any)
				if !ok2 {
					return false
				}
				next, ok := m2[part]
				if !ok {
					return false
				}
				node = next
				continue
			}
			next, ok := m[part]
			if !ok {
				return false
			}
			node = next
		}
		return true
	}
	for _, ref := range regexp.MustCompile(`#/[A-Za-z0-9/_-]+`).FindAllString(openAPIYAML, -1) {
		if !resolve(ref) {
			t.Errorf("悬空引用: %s", ref)
		}
	}
}

// TestOpenAPICoversRoutes 路由表中所有接口都已写入 OpenAPI 文档
func TestOpenAPICoversRoutes(t *testing.T) {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(openAPIYAML), &doc); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`r\.Handle\("([A-Z]+)", "([^"]+)"`)
	have := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		have[m[1]+" "+m[2]] = true
	}
	paths, _ := doc["paths"].(map[any]any)
	docSet := map[string]bool{}
	for p, v := range paths {
		methods, _ := v.(map[any]any)
		for method := range methods {
			if _, ok := method.(string); ok {
				docSet[strings.ToUpper(method.(string))+" "+p.(string)] = true
			}
		}
	}
	var missing []string
	for r := range have {
		if !docSet[normalizeRoute(r)] {
			missing = append(missing, r)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("以下路由未写入 OpenAPI: %v", missing)
	}
}

// normalizeRoute 把 {id} 占位统一为 OpenAPI 的 {id}（本来就是同名，直接返回）
func normalizeRoute(r string) string {
	r = strings.ReplaceAll(r, "{id}", "{id}")
	return r
}
