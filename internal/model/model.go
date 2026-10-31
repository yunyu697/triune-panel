// Package model 定义面板的核心数据模型。
//
// 设计原则（对应 3x-ui 配置失效痛点的根治方案）：
// 数据库只存“用户意图”，内核运行时配置文件是由意图渲染出来的产物，
// 两者严格分离。任何 schema 迁移只增不删，未知字段以 JSON 原样保留。
package model

import "time"

// 三种受支持的内核标识。
const (
	CoreSingBox = "singbox"
	CoreXray    = "xray"
	CoreMihomo  = "mihomo"
)

// Inbound 入站（面板托管的服务端监听）。
// Settings 中的 JSON 键在渲染时按内核透传，面板不理解的字段原样保留，
// 升级面板不会丢弃这些字段。
type Inbound struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Core      string    `json:"core"`     // singbox | xray | mihomo
	Protocol  string    `json:"protocol"` // vless/vmess/trojan/shadowsocks/hysteria2/tuic/mixed/socks...
	Listen    string    `json:"listen"`
	Port      int       `json:"port"`
	Tag       string    `json:"tag"`
	Transport string    `json:"transport"` // tcp/ws/grpc/h2/quic
	TLS       string    `json:"tls"`       // none | tls | reality
	Settings  string    `json:"settings"`  // 透传的协议级配置（JSON 对象）
	Enabled   bool      `json:"enabled"`
	Draft     bool      `json:"draft"` // 导入草稿：未应用前不参与渲染
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Client 面板用户（可订阅多个入站）。
type Client struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	Credential    string     `json:"credential"` // UUID 或密码
	SubToken      string     `json:"sub_token"`  // 订阅令牌
	QuotaGB       float64    `json:"quota_gb"`   // 0 = 不限量
	ExpiresAt     *time.Time `json:"expires_at"`
	RateLimitMbps int        `json:"rate_limit_mbps"` // 预留：三内核暂无原生每用户限速
	Enabled       bool       `json:"enabled"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// ClientInbound 用户-入站关联。
type ClientInbound struct {
	ClientID  int64 `json:"client_id"`
	InboundID int64 `json:"inbound_id"`
}

// ExternalNode 外部节点：仅用于订阅聚合，面板不在本机运行它，
// 导入时不会触碰任何现有服务器配置。
type ExternalNode struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	RawLink   string    `json:"raw_link"` // 原始分享链接，渲染订阅时解析
	CreatedAt time.Time `json:"created_at"`
}

// RevisionStatus 修订状态。
const (
	RevPending    = "pending"             // 已渲染，待校验
	RevFailed     = "failed_validation"   // 校验失败：运行配置未被触碰
	RevApplied    = "applied"             // 已原子替换并重启
	RevSuperseded = "superseded"          // 被更新的修订取代
)

// ConfigRevision 配置修订记录：每次渲染的完整配置快照，
// 升级/回滚/审计都基于它，任何一次变更都可追溯、可还原。
type ConfigRevision struct {
	ID          int64     `json:"id"`
	Core        string    `json:"core"`
	Content     string    `json:"content"` // 渲染后的完整配置
	Hash        string    `json:"hash"`
	Status      string    `json:"status"`
	Note        string    `json:"note"`
	Detail      string    `json:"detail"` // 校验输出等详细信息
	CreatedAt   time.Time `json:"created_at"`
}

// Setting 键值设置（含 schema 版本号、二进制路径等）。
type Setting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
