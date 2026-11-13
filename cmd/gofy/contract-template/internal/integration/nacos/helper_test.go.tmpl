// Package nacos_test keeps the real-contract end-to-end test for registration,
// discovery and typed-client calls. The library owns the resolver unit tests;
// this file only supplies the minimal fake Nacos HTTP surface they share.
// 包 nacos_test 保存真实契约下的注册、发现与类型化客户端端到端测试。
// resolver 单元测试归 api 库所有，本文件只提供共用的最小假 Nacos HTTP 服务。
package nacos_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"sync"

	nacosconfig "github.com/bingcool/gofy/support/nacos"
)

const (
	testListPath     = "/nacos/v1/ns/instance/list"
	testInstancePath = "/nacos/v1/ns/instance"
	testBeatPath     = "/nacos/v1/ns/instance/beat"
)

type fakeNaming struct {
	mu sync.Mutex

	hosts   []nacosconfig.Instance
	calls   int
	queries []url.Values
}

func (f *fakeNaming) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch r.URL.Path {
	case testListPath:
		f.calls++
		f.queries = append(f.queries, r.URL.Query())
		payload, err := json.Marshal(map[string]any{"hosts": f.hosts})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(payload)

	case testInstancePath:
		query := r.URL.Query()
		port, _ := strconv.Atoi(query.Get("port"))
		switch r.Method {
		case http.MethodPost:
			inst := nacosconfig.Instance{
				IP:          query.Get("ip"),
				Port:        port,
				Weight:      1,
				Healthy:     query.Get("healthy") == "true",
				Enabled:     query.Get("enabled") == "true",
				Ephemeral:   query.Get("ephemeral") == "true",
				ClusterName: query.Get("clusterName"),
				ServiceName: query.Get("serviceName"),
				Group:       query.Get("groupName"),
				Namespace:   query.Get("namespaceId"),
			}
			f.hosts = append(f.hosts, inst)
		case http.MethodDelete:
			kept := f.hosts[:0]
			for _, host := range f.hosts {
				if host.IP == query.Get("ip") && host.Port == port {
					continue
				}
				kept = append(kept, host)
			}
			f.hosts = kept
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write([]byte("ok"))

	case testBeatPath:
		_, _ = w.Write([]byte(`{"clientBeatInterval":3000,"lightBeatEnabled":false,"code":10200}`))

	default:
		http.NotFound(w, r)
	}
}
