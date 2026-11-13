package nacos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	instancePath     = "/nacos/v1/ns/instance"
	instanceBeatPath = "/nacos/v1/ns/instance/beat"
	instanceListPath = "/nacos/v1/ns/instance/list"
)

// Instance is a service instance in Nacos naming.
// Instance 是 Nacos 命名服务中的一个服务实例。
type Instance struct {
	IP          string            `json:"ip"`
	Port        int               `json:"port"`
	Weight      float64           `json:"weight"`
	Healthy     bool              `json:"healthy"`
	Enabled     bool              `json:"enabled"`
	Ephemeral   bool              `json:"ephemeral"`
	ClusterName string            `json:"clusterName"`
	ServiceName string            `json:"serviceName"`
	Group       string            `json:"groupName"`
	Namespace   string            `json:"namespaceId"`
	Metadata    map[string]string `json:"metadata"`
}

// InstanceQuery selects the instances returned by ListInstances.
// InstanceQuery 是 ListInstances 的查询条件。
type InstanceQuery struct {
	ServiceName string
	Group       string
	Namespace   string
	Clusters    []string
	HealthyOnly bool
}

// BeatResult is the renewal advice returned by Nacos for a heartbeat.
// BeatResult 是 Nacos 对一次心跳给出的续约建议。
type BeatResult struct {
	// ClientBeatInterval is the interval Nacos asks the client to use.
	// ClientBeatInterval 是 Nacos 要求客户端采用的心跳间隔。
	ClientBeatInterval time.Duration
	// LightBeatEnabled tells the client to omit the beat body next time.
	// LightBeatEnabled 表示下次心跳可以省略请求体。
	LightBeatEnabled bool
}

// NewNamingClient validates only the connection fields of cfg and returns a
// client for the naming API. Unlike NewClient it does not require data_id or
// file, because naming does not read config-center entries.
// NewNamingClient 只校验 cfg 的连接相关字段，返回调用命名服务的客户端。
// 与 NewClient 不同，它不要求 data_id 和 file，因为命名服务不读取配置中心条目。
func NewNamingClient(cfg Config) (*Client, error) {
	cfg = cfg.normalize()
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return nil, fmt.Errorf("nacos: invalid port %d", cfg.Port)
	}
	if cfg.Addr != "" {
		u, err := url.Parse(cfg.Addr)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("nacos: invalid addr %q", cfg.Addr)
		}
	}
	return &Client{cfg: cfg, http: &http.Client{}}, nil
}

// RegisterInstance creates or updates inst. Nacos treats repeated calls for
// the same ip/port as an idempotent registration.
// RegisterInstance 创建或更新 inst。对同一 ip/port 重复调用是幂等注册。
func (c *Client) RegisterInstance(ctx context.Context, inst Instance) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	query := instanceQuery(inst)
	query.Set("weight", strconv.FormatFloat(normalizeWeight(inst.Weight), 'f', -1, 64))
	query.Set("enabled", "true")
	query.Set("healthy", "true")
	if len(inst.Metadata) > 0 {
		metadata, err := json.Marshal(inst.Metadata)
		if err != nil {
			return fmt.Errorf("nacos: encode instance metadata: %w", err)
		}
		query.Set("metadata", string(metadata))
	}
	data, err := c.do(ctx, http.MethodPost, instancePath, query, nil, nil)
	if err != nil {
		return err
	}
	return expectOK(data)
}

// DeregisterInstance removes inst from Nacos. Callers usually ignore the error
// during shutdown because ephemeral instances expire on their own.
// DeregisterInstance 从 Nacos 移除 inst。关闭阶段通常可忽略该错误，因为临时
// 实例在心跳停止后会自行过期。
func (c *Client) DeregisterInstance(ctx context.Context, inst Instance) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	query := instanceQuery(inst)
	data, err := c.do(ctx, http.MethodDelete, instancePath, query, nil, nil)
	if err != nil {
		return err
	}
	return expectOK(data)
}

// BeatInstance sends an ephemeral-instance heartbeat. A light beat omits the
// body and only refreshes the lease; Nacos asks for light beats through
// BeatResult.LightBeatEnabled.
// BeatInstance 为临时实例发送心跳。轻量心跳省略请求体，仅刷新租约；Nacos 通过
// BeatResult.LightBeatEnabled 告知是否改用轻量心跳。
func (c *Client) BeatInstance(ctx context.Context, inst Instance, light bool) (BeatResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	query := instanceQuery(inst)
	if !light {
		beat, err := json.Marshal(beatPayload(inst))
		if err != nil {
			return BeatResult{}, fmt.Errorf("nacos: encode heartbeat: %w", err)
		}
		query.Set("beat", string(beat))
	}
	data, err := c.do(ctx, http.MethodPut, instanceBeatPath, query, nil, nil)
	if err != nil {
		return BeatResult{}, err
	}
	return parseBeat(data)
}

// ListInstances returns the registered hosts of one service. Filtering by
// Enabled/Healthy is left to the caller so it can decide how to handle
// unhealthy instances.
// ListInstances 返回某个服务已注册的主机列表。是否按 Enabled/Healthy 过滤由
// 调用方决定，以便自行处理不健康实例。
func (c *Client) ListInstances(ctx context.Context, q InstanceQuery) ([]Instance, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	query := url.Values{
		"serviceName": {strings.TrimSpace(q.ServiceName)},
		"groupName":   {groupOrDefault(q.Group)},
	}
	if namespace := normalizeNamespace(q.Namespace); namespace != "" {
		query.Set("namespaceId", namespace)
	}
	if len(q.Clusters) > 0 {
		query.Set("clusters", strings.Join(q.Clusters, ","))
	}
	if q.HealthyOnly {
		query.Set("healthyOnly", "true")
	}

	data, err := c.do(ctx, http.MethodGet, instanceListPath, query, nil, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Hosts []Instance `json:"hosts"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("nacos: decode instance list: %w", err)
	}
	return payload.Hosts, nil
}

// AvailableInstances drops hosts that cannot accept traffic.
// AvailableInstances 过滤掉无法承接流量的主机。
func AvailableInstances(instances []Instance) []Instance {
	out := make([]Instance, 0, len(instances))
	for _, inst := range instances {
		if inst.IP == "" || inst.Port <= 0 || !inst.Enabled || !inst.Healthy {
			continue
		}
		out = append(out, inst)
	}
	return out
}

func instanceQuery(inst Instance) url.Values {
	query := url.Values{
		"serviceName": {strings.TrimSpace(inst.ServiceName)},
		"groupName":   {groupOrDefault(inst.Group)},
		"ip":          {strings.TrimSpace(inst.IP)},
		"port":        {strconv.Itoa(inst.Port)},
		"ephemeral":   {strconv.FormatBool(inst.Ephemeral)},
	}
	if namespace := normalizeNamespace(inst.Namespace); namespace != "" {
		query.Set("namespaceId", namespace)
	}
	if cluster := strings.TrimSpace(inst.ClusterName); cluster != "" {
		query.Set("clusterName", cluster)
	}
	return query
}

func beatPayload(inst Instance) map[string]any {
	payload := map[string]any{
		"serviceName": strings.TrimSpace(inst.ServiceName),
		"groupName":   groupOrDefault(inst.Group),
		"ip":          strings.TrimSpace(inst.IP),
		"port":        inst.Port,
		"weight":      normalizeWeight(inst.Weight),
		"ephemeral":   inst.Ephemeral,
	}
	if namespace := normalizeNamespace(inst.Namespace); namespace != "" {
		payload["namespaceId"] = namespace
	}
	if inst.ClusterName != "" {
		payload["cluster"] = inst.ClusterName
	}
	if len(inst.Metadata) > 0 {
		payload["metadata"] = inst.Metadata
	}
	return payload
}

func parseBeat(data []byte) (BeatResult, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return BeatResult{}, nil
	}
	var payload struct {
		ClientBeatInterval int64 `json:"clientBeatInterval"`
		LightBeatEnabled   bool  `json:"lightBeatEnabled"`
		Code               int   `json:"code"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		// Some Nacos versions answer a light beat with a plain "ok".
		// 部分 Nacos 版本对轻量心跳只返回纯文本 "ok"。
		if expectOK(data) == nil {
			return BeatResult{}, nil
		}
		return BeatResult{}, fmt.Errorf("nacos: decode heartbeat response: %w", err)
	}
	if payload.Code != 0 && payload.Code != 10200 {
		return BeatResult{}, fmt.Errorf("nacos: heartbeat rejected: code %d", payload.Code)
	}
	result := BeatResult{LightBeatEnabled: payload.LightBeatEnabled}
	if payload.ClientBeatInterval > 0 {
		result.ClientBeatInterval = time.Duration(payload.ClientBeatInterval) * time.Millisecond
	}
	return result, nil
}

func expectOK(data []byte) error {
	body := strings.TrimSpace(string(data))
	if body == "" || strings.EqualFold(body, "ok") {
		return nil
	}
	return fmt.Errorf("nacos: unexpected response: %s", snippet(data))
}

func normalizeWeight(weight float64) float64 {
	if weight <= 0 {
		return 1
	}
	return weight
}

func groupOrDefault(group string) string {
	group = strings.TrimSpace(group)
	if group == "" {
		return DefaultGroup
	}
	return group
}
