package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"triune/internal/link"
	"triune/internal/model"

	"gopkg.in/yaml.v3"
)

// ==================== 规则集目录与场景预设 ====================

type ruleSetDef struct {
	ID       string `json:"id"`       // geosite-cn / geoip-cn / geosite-netflix ...
	Name     string `json:"name"`     // 显示名
	Dir      string `json:"-"`        // geosite | geoip（决定 URL 模板）
	Behavior string `json:"behavior"` // domain | ipcidr
	Group    string `json:"group"`    // UI 分组：基础分流 / 流媒体 / AI / 广告与隐私 ...
	Cores    string `json:"cores"`    // both | mihomo | singbox（上游规则集覆盖情况）
	// Policy 是勾选该规则集时的默认策略。国内站点走 DIRECT、广告追踪走 REJECT，
	// 前端不再一律按 PROXY 起手（那会把国内站点也塞进代理）。
	Policy string `json:"policy"`
	// AppLog 说明「REJECT 这个集合时，会不会连带拦掉应用自己的日志上报」。
	// 它决定要不要插拦截豁免（blockExemptRules）。每个拦截类集合都必须填，
	// 三选一，空值会被 TestCatalogBlockSetsDeclareAppLog 判红：
	//
	//	appLogNone     不含。拦了不影响应用主流程。
	//	appLogMixed    顺带混进来的。必须插豁免，否则部分 App 首屏卡死。
	//	appLogExplicit 本身就是「拦上报」的集合。用户明确要拦 → 不插豁免，只提示。
	//
	// 为什么放在目录里：判据此前是「列举哪些 tag 要插豁免」，于是「广告域名（精简）」
	// 被漏在外面——上游 category-ads 通过 include:bytedance @ads 一样带着
	// log.zijieapi.com，选了它照样 RST，而豁免一条都不插。新增一个广告类集合时
	// 只需要在这里填一行，判据本身不用动。
	AppLog string `json:"-"`
}

// AppLog 的三个取值。用字符串而不是布尔：将来若出现「半混入」的集合
// （例如只含第三方埋点 SDK），需要的是再补一档，而不是把两个布尔拼起来。
const (
	appLogNone     = "none"
	appLogMixed    = "mixed"
	appLogExplicit = "explicit"
)

type setPolicy struct {
	ID     string `json:"id"`
	Policy string `json:"policy"` // DIRECT | PROXY | REJECT
}

type scenarioDef struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Tag   string      `json:"tag"` // 场景分类标签：通用 / 流媒体 / AI / 游戏 ...
	Desc  string      `json:"desc"`
	Sets  []setPolicy `json:"sets"`
	Rules []string    `json:"rules"` // 场景内置的自定义规则（clash 语法）
}

// ruleCatalog 规则集目录（geosite 类目名与 v2fly domain-list-community / MetaCubeX / SagerNet 对齐）
// ruleCatalog 规则集目录
// 所有条目均已对上游真实文件校验：
//
//	mihomo   → MetaCubeX/meta-rules-dat  meta/geo/{geosite|geoip}/{name}.mrs
//	sing-box → SagerNet/sing-geosite      rule-set/geosite-{name}.srs
//	           SagerNet/sing-geoip       rule-set/geoip-{name}.srs
//
// Cores 标记该规则集在哪些内核的上游存在（geoip-private / category-porn 仅 mihomo 提供）。
var ruleCatalog = []ruleSetDef{
	{ID: "geosite-cn", Name: "中国大陆域名", Dir: "geosite", Behavior: "domain", Group: "基础分流", Cores: "both", Policy: "DIRECT"},
	{ID: "geoip-cn", Name: "中国大陆 IP", Dir: "geoip", Behavior: "ipcidr", Group: "基础分流", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-private", Name: "局域网 / 私有域名", Dir: "geosite", Behavior: "domain", Group: "基础分流", Cores: "both", Policy: "DIRECT"},
	{ID: "geoip-private", Name: "局域网 / 私有 IP", Dir: "geoip", Behavior: "ipcidr", Group: "基础分流", Cores: "mihomo", Policy: "DIRECT"},
	{ID: "geosite-geolocation-!cn", Name: "非中国大陆域名", Dir: "geosite", Behavior: "domain", Group: "基础分流", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-ads-all", Name: "广告域名（通用全量）", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both", Policy: "REJECT", AppLog: appLogMixed},
	// 精简集只收「广告投放」类，但上游 category-ads 里 include:bytedance @ads 照样带着
	// log.zijieapi.com——这也是实测被拒过的那个域名。所以它和全量集是同一类，不是「干净版」。
	{ID: "geosite-category-ads", Name: "广告域名（精简）", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both", Policy: "REJECT", AppLog: appLogMixed},
	// 追踪器分类里没有实证到应用自有埋点，但误插豁免的代价只是「applog / zijieapi 走直连」
	// （本来就该直连），而漏插的代价是首屏卡死。代价不对称，所以按 mixed 处理。
	{ID: "geosite-category-public-tracker", Name: "公共追踪器", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both", Policy: "REJECT", AppLog: appLogMixed},
	// 遥测集里明确列着 fqnovel / bytedance 的 applog 主机。用户勾它就是要把这些拦掉，
	// 所以不插豁免——但必须提示他首屏会卡，见 explicitBlockWarn。
	{ID: "geosite-category-telemetry", Name: "遥测 / 数据上报", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both", Policy: "REJECT", AppLog: appLogExplicit},
	{ID: "geosite-category-consent-management", Name: "Cookie 同意管理", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both", Policy: "REJECT", AppLog: appLogNone},
	{ID: "geosite-category-password-management", Name: "密码管理器", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-ip-geo-detect", Name: "IP 归属地检测", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both", Policy: "REJECT", AppLog: appLogNone},
	{ID: "geosite-category-anticensorship", Name: "反审查站点", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-proxy-tunnels", Name: "代理隧道与中转", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-vpnservices", Name: "VPN 服务商", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-stun", Name: "STUN / TURN 服务", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both", Policy: "REJECT", AppLog: appLogNone},
	{ID: "geosite-category-ipfs", Name: "IPFS 节点", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-openai", Name: "OpenAI / ChatGPT", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-anthropic", Name: "Anthropic / Claude", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-ai-!cn", Name: "AI 服务（海外）", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-ai-chat-!cn", Name: "AI 对话（海外）", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-ai-cn", Name: "AI 服务（国内）", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-ai-ru", Name: "AI 服务（俄罗斯区）", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-google-gemini", Name: "Google Gemini", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-perplexity", Name: "Perplexity", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-deepseek", Name: "DeepSeek", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-doubao", Name: "豆包 Doubao", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-bing", Name: "Bing / Copilot", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-huggingface", Name: "Hugging Face", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-notion", Name: "Notion", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-netflix", Name: "Netflix", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-disney", Name: "Disney+", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-youtube", Name: "YouTube", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-tiktok", Name: "TikTok", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-spotify", Name: "Spotify", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-hbo", Name: "HBO", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-bahamut", Name: "巴哈姆特动漫", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-twitch", Name: "Twitch", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-netease", Name: "网易云音乐", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-bilibili", Name: "哔哩哔哩", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-iqiyi", Name: "爱奇艺", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-youku", Name: "优酷", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-media", Name: "影音媒体站点", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-media-cn", Name: "国内影音站点", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-entertainment", Name: "娱乐站点", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-entertainment-cn", Name: "国内娱乐站点", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-telegram", Name: "Telegram", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-twitter", Name: "X / Twitter", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-facebook", Name: "Facebook", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-instagram", Name: "Instagram", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-whatsapp", Name: "WhatsApp", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-line", Name: "LINE", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-discord", Name: "Discord", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-reddit", Name: "Reddit", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-viber", Name: "Viber", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-signal", Name: "Signal", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-games", Name: "游戏平台（综合）", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-games-cn", Name: "国内游戏平台", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-game-accelerator-cn", Name: "国内游戏加速服务", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-enhance-gaming", Name: "游戏加速增强", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-flight-simulator", Name: "模拟器类游戏", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-steam", Name: "Steam", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-epicgames", Name: "Epic Games", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-playstation", Name: "PlayStation", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-xbox", Name: "Xbox", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-nintendo", Name: "Nintendo", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-blizzard", Name: "暴雪战网", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-mihoyo", Name: "米哈游（HoYoPlay）", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-github", Name: "GitHub", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-gitlab", Name: "GitLab", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-docker", Name: "Docker Hub", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-golang", Name: "Go 语言站点", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-nodejs", Name: "Node.js / npm", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-jetbrains", Name: "JetBrains", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-dev", Name: "开发者站点", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-dev-cn", Name: "国内开发者站点", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-cdn-!cn", Name: "海外 CDN", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-cdn-cn", Name: "国内 CDN", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-finance", Name: "金融理财站点", Dir: "geosite", Behavior: "domain", Group: "金融商务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-bank-cn", Name: "国内银行", Dir: "geosite", Behavior: "domain", Group: "金融商务", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-securities-cn", Name: "国内证券", Dir: "geosite", Behavior: "domain", Group: "金融商务", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-cryptocurrency", Name: "加密货币", Dir: "geosite", Behavior: "domain", Group: "金融商务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-paypal", Name: "PayPal", Dir: "geosite", Behavior: "domain", Group: "金融商务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-stripe", Name: "Stripe", Dir: "geosite", Behavior: "domain", Group: "金融商务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-wise", Name: "Wise 转账", Dir: "geosite", Behavior: "domain", Group: "金融商务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-amazon", Name: "Amazon", Dir: "geosite", Behavior: "domain", Group: "购物电商", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-ebay", Name: "eBay", Dir: "geosite", Behavior: "domain", Group: "购物电商", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-pinduoduo", Name: "拼多多", Dir: "geosite", Behavior: "domain", Group: "购物电商", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-ecommerce", Name: "电商平台", Dir: "geosite", Behavior: "domain", Group: "购物电商", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-microsoft", Name: "Microsoft", Dir: "geosite", Behavior: "domain", Group: "办公云服务", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-google", Name: "Google", Dir: "geosite", Behavior: "domain", Group: "办公云服务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-dropbox", Name: "Dropbox", Dir: "geosite", Behavior: "domain", Group: "办公云服务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-zoom", Name: "Zoom 会议", Dir: "geosite", Behavior: "domain", Group: "办公云服务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-onedrive", Name: "OneDrive", Dir: "geosite", Behavior: "domain", Group: "办公云服务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-icloud", Name: "iCloud", Dir: "geosite", Behavior: "domain", Group: "办公云服务", Cores: "both", Policy: "DIRECT"},
	// Apple 生态内容服务（App Store / Apple Music / Apple TV+ 等）的可选规则集。
	// 系统更新 / 激活 / 登录等关键域名已由 systemDirectRules 无条件直连且在规则集
	// 之前，所以这里即便被用户改成 PROXY，也不会把 mesu.apple.com / appleid 之类
	// 吸进代理——只影响真正的内容服务。默认 DIRECT 与 iCloud / Microsoft 口径一致。
	{ID: "geosite-apple", Name: "Apple 服务", Dir: "geosite", Behavior: "domain", Group: "办公云服务", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-companies", Name: "大型企业站点", Dir: "geosite", Behavior: "domain", Group: "办公云服务", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-scholar-cn", Name: "国内学术资源", Dir: "geosite", Behavior: "domain", Group: "教育科研", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-scholar-!cn", Name: "海外学术资源", Dir: "geosite", Behavior: "domain", Group: "教育科研", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-scholar-hk", Name: "港澳学术资源", Dir: "geosite", Behavior: "domain", Group: "教育科研", Cores: "both", Policy: "PROXY"},
	{ID: "geosite-category-mooc-cn", Name: "国内慕课平台", Dir: "geosite", Behavior: "domain", Group: "教育科研", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-education-cn", Name: "国内教育站点", Dir: "geosite", Behavior: "domain", Group: "教育科研", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-documents-cn", Name: "国内文档站", Dir: "geosite", Behavior: "domain", Group: "教育科研", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-hospital-cn", Name: "国内医院与医疗", Dir: "geosite", Behavior: "domain", Group: "生活服务", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-food-cn", Name: "国内餐饮外卖", Dir: "geosite", Behavior: "domain", Group: "生活服务", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-logistics-cn", Name: "国内物流", Dir: "geosite", Behavior: "domain", Group: "生活服务", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-blog-cn", Name: "国内博客", Dir: "geosite", Behavior: "domain", Group: "生活服务", Cores: "both", Policy: "DIRECT"},
	{ID: "geosite-category-porn", Name: "成人内容（拦截用）", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "mihomo", Policy: "REJECT", AppLog: appLogNone},
}

var ruleSetIndex = func() map[string]ruleSetDef {
	m := map[string]ruleSetDef{}
	for _, d := range ruleCatalog {
		m[d.ID] = d
	}
	return m
}()

// scenarioPresets 场景预设（最常用的几种）
// scenarioPresets 场景预设（按使用场景一键套用规则集与策略）
var scenarioPresets = []scenarioDef{
	{
		ID: "balanced", Name: "均衡日常", Tag: "通用",
		Desc: "国内直连 + 广告拦截 + 常规外网走代理，绝大多数用户的默认选择",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
			{ID: "geosite-microsoft", Policy: "DIRECT"},
			{ID: "geosite-google", Policy: "PROXY"},
		},
	},
	{
		ID: "streaming", Name: "流媒体优先", Tag: "流媒体",
		Desc: "海外流媒体全量走代理，避免 CDN 落地变慢、区域限制",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-netflix", Policy: "PROXY"},
			{ID: "geosite-disney", Policy: "PROXY"},
			{ID: "geosite-youtube", Policy: "PROXY"},
			{ID: "geosite-tiktok", Policy: "PROXY"},
			{ID: "geosite-spotify", Policy: "PROXY"},
			{ID: "geosite-hbo", Policy: "PROXY"},
			{ID: "geosite-bahamut", Policy: "PROXY"},
			{ID: "geosite-twitch", Policy: "PROXY"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
			{ID: "geosite-microsoft", Policy: "DIRECT"},
		},
	},
	{
		ID: "gaming", Name: "游戏加速", Tag: "游戏",
		Desc: "游戏平台与加速服务走代理，降低延迟；国内服务照常直连",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-category-games", Policy: "PROXY"},
			{ID: "geosite-steam", Policy: "PROXY"},
			{ID: "geosite-epicgames", Policy: "PROXY"},
			{ID: "geosite-playstation", Policy: "PROXY"},
			{ID: "geosite-xbox", Policy: "PROXY"},
			{ID: "geosite-nintendo", Policy: "PROXY"},
			{ID: "geosite-blizzard", Policy: "PROXY"},
			{ID: "geosite-mihoyo", Policy: "PROXY"},
			{ID: "geosite-category-enhance-gaming", Policy: "PROXY"},
			{ID: "geosite-category-flight-simulator", Policy: "PROXY"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
		},
	},
	{
		ID: "ai", Name: "AI 服务", Tag: "AI",
		Desc: "ChatGPT / Claude / Gemini 等走代理保证区域可用，国内 AI 服务直连",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-openai", Policy: "PROXY"},
			{ID: "geosite-anthropic", Policy: "PROXY"},
			{ID: "geosite-category-ai-!cn", Policy: "PROXY"},
			{ID: "geosite-category-ai-chat-!cn", Policy: "PROXY"},
			{ID: "geosite-google-gemini", Policy: "PROXY"},
			{ID: "geosite-perplexity", Policy: "PROXY"},
			{ID: "geosite-deepseek", Policy: "DIRECT"},
			{ID: "geosite-huggingface", Policy: "PROXY"},
			{ID: "geosite-notion", Policy: "PROXY"},
			{ID: "geosite-bing", Policy: "PROXY"},
			{ID: "geosite-category-ai-cn", Policy: "DIRECT"},
			{ID: "geosite-doubao", Policy: "DIRECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
		},
	},
	{
		ID: "privacy", Name: "隐私优先", Tag: "隐私",
		Desc: "广告 / 追踪 / 遥测全部拦截，除国内必需外一律走代理",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-category-public-tracker", Policy: "REJECT"},
			{ID: "geosite-category-telemetry", Policy: "REJECT"},
			{ID: "geosite-category-consent-management", Policy: "REJECT"},
			{ID: "geosite-category-password-management", Policy: "REJECT"},
			{ID: "geosite-category-ip-geo-detect", Policy: "REJECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
		},
		Rules: []string{
			"DOMAIN-KEYWORD,analytics,REJECT",
			"DOMAIN-KEYWORD,tracker,REJECT",
			"DOMAIN-SUFFIX,doubleclick.net,REJECT",
		},
	},
	{
		ID: "cn-max", Name: "国内直连优先", Tag: "通用",
		Desc: "只有明确在境外的站点走代理，可用性最高、延迟最低",
		Sets: []setPolicy{
			{ID: "geosite-geolocation-!cn", Policy: "PROXY"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
		},
	},
	{
		ID: "work", Name: "办公协作", Tag: "办公",
		Desc: "SaaS 协作工具走代理，保证海外团队会议与文档同步顺畅",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-zoom", Policy: "PROXY"},
			{ID: "geosite-dropbox", Policy: "PROXY"},
			{ID: "geosite-onedrive", Policy: "PROXY"},
			{ID: "geosite-microsoft", Policy: "PROXY"},
			{ID: "geosite-google", Policy: "PROXY"},
			{ID: "geosite-category-companies", Policy: "PROXY"},
			{ID: "geosite-category-cdn-!cn", Policy: "PROXY"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
		},
	},
	{
		ID: "dev", Name: "开发调试", Tag: "开发",
		Desc: "代码托管 / 镜像源 / 包管理走代理，国内镜像直连",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-github", Policy: "PROXY"},
			{ID: "geosite-gitlab", Policy: "PROXY"},
			{ID: "geosite-docker", Policy: "PROXY"},
			{ID: "geosite-golang", Policy: "PROXY"},
			{ID: "geosite-nodejs", Policy: "PROXY"},
			{ID: "geosite-jetbrains", Policy: "PROXY"},
			{ID: "geosite-category-dev", Policy: "PROXY"},
			{ID: "geosite-category-dev-cn", Policy: "DIRECT"},
			{ID: "geosite-category-cdn-cn", Policy: "DIRECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
		},
	},
	{
		ID: "study", Name: "学术科研", Tag: "学习",
		Desc: "海外学术资源走代理，国内教务与文档站直连",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-category-scholar-!cn", Policy: "PROXY"},
			{ID: "geosite-category-scholar-cn", Policy: "DIRECT"},
			{ID: "geosite-category-scholar-hk", Policy: "DIRECT"},
			{ID: "geosite-category-mooc-cn", Policy: "DIRECT"},
			{ID: "geosite-category-education-cn", Policy: "DIRECT"},
			{ID: "geosite-category-documents-cn", Policy: "DIRECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
		},
	},
	{
		ID: "finance", Name: "金融证券", Tag: "商务",
		Desc: "海外支付与理财走代理，国内银行证券直连",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-category-finance", Policy: "PROXY"},
			{ID: "geosite-paypal", Policy: "PROXY"},
			{ID: "geosite-stripe", Policy: "PROXY"},
			{ID: "geosite-wise", Policy: "PROXY"},
			{ID: "geosite-category-cryptocurrency", Policy: "PROXY"},
			{ID: "geosite-category-bank-cn", Policy: "DIRECT"},
			{ID: "geosite-category-securities-cn", Policy: "DIRECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
		},
	},
	{
		ID: "shopping", Name: "海淘电商", Tag: "商务",
		Desc: "海外电商走代理保物流与风控稳定，国内电商直连",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-amazon", Policy: "PROXY"},
			{ID: "geosite-ebay", Policy: "PROXY"},
			{ID: "geosite-category-ecommerce", Policy: "PROXY"},
			{ID: "geosite-pinduoduo", Policy: "DIRECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
		},
	},
	{
		ID: "social", Name: "社交通讯", Tag: "社交",
		Desc: "Telegram / X / Discord 等社交平台走代理，保留国内即时通讯直连",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-telegram", Policy: "PROXY"},
			{ID: "geosite-twitter", Policy: "PROXY"},
			{ID: "geosite-facebook", Policy: "PROXY"},
			{ID: "geosite-instagram", Policy: "PROXY"},
			{ID: "geosite-whatsapp", Policy: "PROXY"},
			{ID: "geosite-line", Policy: "PROXY"},
			{ID: "geosite-discord", Policy: "PROXY"},
			{ID: "geosite-reddit", Policy: "PROXY"},
			{ID: "geosite-signal", Policy: "PROXY"},
			{ID: "geosite-viber", Policy: "PROXY"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
		},
	},
	{
		ID: "video-call", Name: "视频会议", Tag: "办公",
		Desc: "会议与会议相关 SaaS 走代理，注意保留 UDP 以保证实时画质",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-zoom", Policy: "PROXY"},
			{ID: "geosite-microsoft", Policy: "PROXY"},
			{ID: "geosite-google", Policy: "PROXY"},
			{ID: "geosite-category-companies", Policy: "PROXY"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
		},
		Rules: []string{
			"DOMAIN-SUFFIX,meet.google.com,PROXY",
		},
	},
	{
		ID: "low-latency", Name: "低延迟精简", Tag: "通用",
		Desc: "只保留必要规则集，规则匹配开销最小，适合小内存路由与 NAS",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
			{ID: "geosite-geolocation-!cn", Policy: "PROXY"},
		},
	},
	{
		ID: "ads-block", Name: "纯净拦截", Tag: "隐私",
		Desc: "专注拦广告，其余按国内直连 / 境外代理分流",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-category-ads", Policy: "REJECT"},
			{ID: "geosite-category-public-tracker", Policy: "REJECT"},
			{ID: "geosite-category-telemetry", Policy: "REJECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
			{ID: "geosite-geolocation-!cn", Policy: "PROXY"},
		},
	},
	{
		ID: "overseas", Name: "出海商务", Tag: "商务",
		Desc: "除中国大陆外全部走代理，适合出海办公与海外业务",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-geolocation-!cn", Policy: "PROXY"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
		},
	},
	{
		ID: "back-to-cn", Name: "回国加速", Tag: "通用",
		Desc: "国内站点与国内 CDN 强制直连，境外走代理，适合回国使用",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-category-cdn-cn", Policy: "DIRECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
			{ID: "geosite-geolocation-!cn", Policy: "PROXY"},
		},
	},
	{
		ID: "kid-safe", Name: "青少年模式", Tag: "隐私",
		Desc: "拦截成人内容与广告追踪，其余保持国内直连",
		Sets: []setPolicy{
			{ID: "geosite-category-porn", Policy: "REJECT"},
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-category-public-tracker", Policy: "REJECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
		},
	},
	{
		ID: "music", Name: "音乐听歌", Tag: "流媒体",
		Desc: "海外音乐平台走代理，国内音乐 App 直连",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-spotify", Policy: "PROXY"},
			{ID: "geosite-youtube", Policy: "PROXY"},
			{ID: "geosite-netease", Policy: "DIRECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
		},
	},
	{
		ID: "anime", Name: "动漫追番", Tag: "流媒体",
		Desc: "B 站与巴哈姆特等正版平台，国内平台直连不卡顿",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-bilibili", Policy: "DIRECT"},
			{ID: "geosite-bahamut", Policy: "PROXY"},
			{ID: "geosite-youtube", Policy: "PROXY"},
			{ID: "geosite-netease", Policy: "DIRECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
		},
	},
	{
		ID: "news", Name: "资讯聚合", Tag: "生活",
		Desc: "海外媒体走代理，国内资讯与博客直连",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-category-media", Policy: "PROXY"},
			{ID: "geosite-category-media-cn", Policy: "DIRECT"},
			{ID: "geosite-category-blog-cn", Policy: "DIRECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
		},
	},
	{
		ID: "smart-home", Name: "智能家居直连", Tag: "生活",
		Desc: "局域网与国内 IoT / 生活服务全部直连，只把境外流量交给代理",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-category-food-cn", Policy: "DIRECT"},
			{ID: "geosite-category-hospital-cn", Policy: "DIRECT"},
			{ID: "geosite-category-logistics-cn", Policy: "DIRECT"},
		},
	},
	{
		ID: "global", Name: "全局代理", Tag: "通用",
		Desc: "除中国大陆外所有流量统一走代理，规则最简洁",
		Sets: []setPolicy{
			{ID: "geosite-category-ads-all", Policy: "REJECT"},
			{ID: "geosite-geolocation-!cn", Policy: "PROXY"},
			{ID: "geosite-cn", Policy: "DIRECT"},
			{ID: "geoip-cn", Policy: "DIRECT"},
			{ID: "geosite-private", Policy: "DIRECT"},
		},
	},
}

// ==================== 生成请求/响应 ====================

type customSetReq struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Behavior string `json:"behavior"` // domain | ipcidr | classical
	Policy   string `json:"policy"`
	Interval int    `json:"interval"` // 秒，默认 86400
}

type genReq struct {
	Core string `json:"core"` // mihomo | singbox
	// CoreVersion 仅 sing-box 有意义：1.11 与 1.12+ 的配置结构不兼容，
	// 必须知道目标版本才能生成正确的 JSON（见 sbversion.go 的实测表）。
	// mihomo 忽略此字段——它的 url-test / rule-providers 语法各版本稳定。
	CoreVersion string         `json:"core_version"`
	Target      string         `json:"target"`
	InboundIDs  []int64        `json:"inbound_ids"`
	Scenario    string         `json:"scenario"`
	Sets        []setPolicy    `json:"sets"`
	CustomRules []string       `json:"custom_rules"`
	CustomSets  []customSetReq `json:"custom_sets"`
	// RuleMirror / RuleMirrorPrefix 规则集下载源（见 rulemirror.go）。
	// 留空 = 官方直连，与 v1.1.2 及以前的行为一致——不能因为发了新功能
	// 就让老用户已经跑通的配置悄悄换下载地址。
	RuleMirror       string `json:"rule_mirror"`
	RuleMirrorPrefix string `json:"rule_mirror_prefix"`
	// LANDNS 局域网专用域名（.lan / .local / .home.arpa / .localdomain）交给谁解析，
	// 见 lanResolver。
	//
	// 用指针是为了分清「没带这个字段」和「明确清空」：
	//   - nil：客户端没提这件事。可能是 v2.1.8 之前保存的配置，重新生成时
	//     按当前设置走——「重新生成」应当是「按现在的偏好重建」，而不是悄悄
	//     丢掉用户后来填过的设置；
	//   - ""：用户把输入框清空了，就是不要这条策略，不去读设置。
	LANDNS *string `json:"lan_dns"`
	// BypassServer 自动把导出节点所用的服务器地址写成最高优先级 DIRECT 规则。
	//
	// 主路由 / 旁路由 / TUN 接管全机流量后，代理客户端「连节点服务器本身」的
	// 那条连接也会被 MATCH,PROXY 吸进隧道，变成「用隧道去连隧道端点」的回环：
	// 配置能正常加载、节点却永远握不上手，日志里只有超时。地址由面板从
	// publicHost 自动带出，不需要用户手填。
	//
	// 默认 false：老版本 UI / 直接调 API 的脚本不带这个字段时保持原行为，
	// 不悄悄改写别人已经跑通的分流规则。前端默认勾选。
	BypassServer bool `json:"bypass_server"`
}

// mirror 归一请求里的下载源选择（非法值退回官方，理由见 resolveMirror）。
func (q genReq) mirror() ruleMirrorSel {
	s, _ := resolveMirror(q.RuleMirror, q.RuleMirrorPrefix)
	return s
}

// genOpts 是生成器的可选输入。
//
// 收成一个结构体而不是继续往参数表上挂：这台生成器已经有五个必填参数
// （目标 / 版本 / 节点 / 规则集 / 自定义规则），可选行为只会越来越多——
// 绕过节点服务器、下载源、局域网 DNS，以后还有别的。每加一个就改 70 处
// 调用点，改的时候还得逐处确认参数顺序，代价与收益完全不成比例；
// 而且漏改一处就是编译错误，但顺序写反了不会报错。
type genOpts struct {
	Mirror ruleMirrorSel
	Bypass bypassSpec
	LAN    lanResolver
}

// handleExportCatalog GET /api/export/catalog：规则集目录 + 场景预设 + 下载源列表
func (a *Server) handleExportCatalog(w http.ResponseWriter, r *http.Request) {
	// 默认下载源跟着设置走：用户上次选过镜像，下次打开生成页就还是它，
	// 不用每次重新选（这个选择在全球网络环境下不会天天变）。
	defMirror, _ := a.Store.GetSetting(MirrorSetting)
	if defMirror == "" {
		defMirror = MirrorOfficial
	}
	defPrefix, _ := a.Store.GetSetting(MirrorPrefixSetting)
	// 局域网 DNS 同样回填：填过一次就不该每次都重新输。空值是正常的初始状态
	// （大多数家庭网络根本不用 .lan 名字），所以不设默认值。
	defLAN, _ := a.Store.GetSetting(SettingLANDNS)
	writeJSON(w, 200, map[string]any{
		"sets":      ruleCatalog,
		"scenarios": scenarioPresets,
		"mirrors":   ruleMirrors,
		"mirror":    map[string]string{"id": defMirror, "prefix": defPrefix},
		"lan_dns":   defLAN,
		// classes 是「哪类关键服务、由哪些规则集覆盖」的对照表。前端拿它把
		// 用户勾选的规则集翻译成「这一类的保障来自哪」——少勾一个 geosite-apple
		// 时，界面要能说清苹果那一类现在只剩系统清单兜底。
		"classes": systemClasses,
	})
}

// handleExportConfig POST /api/export/config：生成完整客户端配置
// 返回 {filename, content, warnings}
func (a *Server) handleExportConfig(w http.ResponseWriter, r *http.Request) {
	var q genReq
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		fail(w, 400, "bad request")
		return
	}
	if q.Core != "mihomo" && q.Core != "singbox" {
		fail(w, 400, "core 必须是 mihomo 或 singbox")
		return
	}
	if !validGenTarget(q.Target) {
		fail(w, 400, "target 必须是 client / main-router / side-router / openwrt-momo")
		return
	}
	// momo 跑的是 sing-box，profile 就是一份 config.json；给 mihomo 的 YAML
	// 它连解析都不会，静默「替用户改成 sing-box」只会让人以为自己勾错了。
	if q.Target == genTargetMomo && q.Core != "singbox" {
		fail(w, 400, "OpenWrt-momo 基于 sing-box：请把配置内核切成 sing-box（mihomo 的 YAML 不能作为 momo 的 profile）")
		return
	}
	// 版本在这里先定下来：它是「这份请求本身」的合法性，不该等到收集完节点
	// 才检查。否则用户选错版本时看到的可能是「没有有效用户」，把问题指到
	// 完全无关的地方。
	ver := defaultSBVersion
	if q.Core == "singbox" {
		var err error
		ver, err = parseSBVersion(q.CoreVersion)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		if !ver.atLeast(minSBVersion.Major, minSBVersion.Minor) {
			fail(w, 400, fmt.Sprintf(
				"sing-box %s 不支持：1.10 及以前没有路由 action，配置结构完全不同（block/dns 出站 + inet4_address）。请选 1.11 或更高。", ver))
			return
		}
		// momo 的依赖写死 sing-box >= 1.12，它的 mixin 会往 profile 里塞
		// 1.12 才有意义的 dns / cache_file 字段；1.11 的旧 DNS address 格式
		// 与 fakeip type 都对不上，生成的配置「能存进去、起不来」。
		if q.Target == genTargetMomo && !ver.atLeast(1, 12) {
			fail(w, 400, "OpenWrt-momo 需要 sing-box 1.12 或更高（1.11 的 DNS 格式与 momo 的 mixin 不兼容），请重新选择版本")
			return
		}
	}
	if len(q.InboundIDs) == 0 {
		fail(w, 400, "请至少选择一个入站")
		return
	}

	// 收集节点（入站 × 有效用户）
	host := a.publicHost(r)
	ins, _ := a.Store.ListInbounds("")
	byID := map[int64]*model.Inbound{}
	for _, in := range ins {
		byID[in.ID] = in
	}
	clients, _ := a.Store.ListClients()
	type pair struct {
		in *model.Inbound
		c  *model.Client
	}
	var pairs []pair
	for _, id := range q.InboundIDs {
		in, ok := byID[id]
		if !ok {
			continue
		}
		for _, c := range clients {
			if !c.Enabled || c.ExpiresAt != nil && c.ExpiresAt.Before(timeNow()) {
				continue
			}
			ids, _ := a.Store.ClientInboundIDs(c.ID)
			for _, cid := range ids {
				if cid == id {
					pairs = append(pairs, pair{in, c})
					break
				}
			}
		}
	}
	if len(pairs) == 0 {
		fail(w, 422, "所选入站下没有有效用户（用户需已创建、启用且勾选该入站）")
		return
	}
	var nodes []*link.Node
	for _, p := range pairs {
		n := a.inboundToNode(p.in, p.c, host)
		n.Name = p.in.Name + "-" + p.c.Name
		nodes = append(nodes, n)
	}
	// 入站名 / 用户名都可能重复，节点名撞车会让内核判「重复 tag」，见 dedupeNodeNames
	dedupeNodeNames(nodes)

	// 解析规则集选择 + 自定义规则集
	sets, warns := normalizeSets(q)

	// 自动直连节点服务器：地址直接从这批导出节点里取，与配置里 proxies 的
	// server 是同一个值，不会出现「规则写 A、客户端连 B」的错位。
	var bypass bypassSpec
	if q.BypassServer {
		bypass = collectBypassHosts(nodes)
		if bypass.empty() {
			warns = append(warns, "已开启「绕过节点服务器」，但节点没有可用地址，未生成直连规则")
		}
	}

	// 客户端配置里的 server 来自「入站的节点地址」，没指定时才回落到 publicHost。
	// 没填「公网地址」而管理员又是用 127.0.0.1 / localhost 打开的面板时，导出的
	// 节点会指向回环地址——拿到另一台机器上永远连不上，而配置本身每一项都是对的，
	// 用户只能反复怀疑节点。
	// 这里按**实际生成的节点**判断，不看全局值：入站可以各自指定地址，只要所有
	// 节点都是公网地址就不该提示。只提示不阻断：面板和客户端同机确实能这么用。
	if q.Target == "client" {
		if bad := loopbackNodeHosts(nodes); len(bad) > 0 {
			warns = append(warns, fmt.Sprintf(
				"导出的节点地址是 %s（本机回环），只有客户端和面板在同一台机器上才连得通。"+
					"请到「设置 → 节点公开地址」填服务器的公网 IP 或域名，或在入站的「节点地址」里单独指定后重新导出。",
				strings.Join(bad, "、")))
		}
	}

	// 下载源：非法值已在 resolveMirror 里退回官方，这里只把原因转成提示
	mir := q.mirror()
	if _, warn := resolveMirror(q.RuleMirror, q.RuleMirrorPrefix); warn != "" {
		warns = append(warns, warn)
	}

	// 局域网 DNS：值来自「设置」，前端每次导出都回传（同下载源）。
	//
	// 这里**不**因为值不合法就 400：库里可能躺着一条旧版本 / 手改数据库留下的
	// 脏值，拒绝导出等于把用户锁死在「一份配置也导不出来」的状态，而他要的多半
	// 只是先拿到一份能用的配置。退回空、把原因写进警告，症状清楚也不挡路；
	// 真正的拦截在保存设置那一步（putSettings），脏值进不了库。
	lanRaw := ""
	switch {
	case q.LANDNS != nil:
		lanRaw = *q.LANDNS
	default:
		// 老配置（保存时还没有这个字段）与脚本调用：按当前设置走。
		lanRaw, _ = a.Store.GetSetting(SettingLANDNS)
	}
	lan, lanBad := parseLANResolver(lanRaw)
	if lanBad != "" {
		warns = append(warns, "「局域网 DNS」设置无效，本次已忽略（"+lanBad+"）")
	}
	opts := genOpts{Mirror: mir, Bypass: bypass, LAN: lan}

	// 生成前先确认「至少有一个节点真的能导出」，理由见 exportable.go。
	// 一个都导不出时**不要**生成——空 proxies 的配置内核只会拒绝，
	// 用户还得反过来猜是哪一步错了。
	skipped, exportable := splitExport(nodes, q.Core)
	if exportable == 0 {
		fail(w, 422, noExportMsg(skipped, q.Core))
		return
	}
	if len(skipped) > 0 {
		warns = append(warns, fmt.Sprintf(
			"以下入站不能导出为 %s 节点，已跳过：%s",
			coreLabel(q.Core), strings.Join(skipped, "、")))
	}

	// ver 已在请求校验时定好（1.11 与 1.12+ 是两套 JSON）；这里只补上
	// 「这份配置按哪个版本写的」提示，让用户下载后还能对得上号。
	if q.Core == "singbox" {
		if note := versionNote(ver); note != "" {
			warns = append(warns, "sing-box "+ver.String()+"："+note)
		}
	}

	var g *generator
	switch {
	case q.Target == genTargetMomo:
		g = genMomo(ver, nodes, sets, q.CustomRules, &warns, opts)
	case q.Core == "mihomo":
		g = genMihomo(q.Target, nodes, sets, q.CustomRules, &warns, opts)
	default:
		g = genSingbox(q.Target, ver, nodes, sets, q.CustomRules, &warns, opts)
	}

	var content string
	ct := "application/json"
	if q.Core == "mihomo" {
		body, _ := yaml.Marshal(g.doc)
		content = g.header + string(body)
		ct = "text/yaml"
	} else {
		out, _ := json.MarshalIndent(g.doc, "", "  ")
		content = string(out)
	}
	safeName := "config-" + q.Target + "-" + q.Scenario
	if safeName == "config--" {
		safeName = "config"
	}
	resp := map[string]any{
		"filename":     fmt.Sprintf("%s-%s.%s", safeName, q.Core, map[string]string{"mihomo": "yaml", "singbox": "json"}[q.Core]),
		"content":      content,
		"content_type": ct,
		"warnings":     warns,
		// 回传实际生效的下载源：前端要显示「这份配置的规则集走 gh-proxy.com」，
		// 而不是让用户自己在几百行 YAML 里翻 rule-providers 的 url 才看得出来。
		"rule_mirror":  mir.ID,
		"mirror_label": mir.label(),
	}
	// 回显实际直连了哪些地址：TUN 模式下这几行规则决定「能不能连上节点」，
	// 用户需要能在页面上一眼核对，而不是去几百行 YAML 里翻。
	if q.BypassServer {
		resp["bypass_servers"] = bypass.hosts()
	}
	// 版本信息回传给前端显示：用户得知道这份配置是给哪个版本写的，
	// 否则拿 1.14 的 JSON 去喂 1.11 只会得到一句看不懂的 FATAL。
	if q.Core == "singbox" {
		resp["core_version"] = ver.String()
		resp["version_note"] = versionNote(ver)
	}
	writeJSON(w, 200, resp)
}

// ==================== 规则集归一化 ====================

type normSet struct {
	Tag      string // provider/rule_set 标识
	URL      string
	Behavior string // domain | ipcidr | classical
	Policy   string // DIRECT | PROXY | REJECT
	Format   string // mrs | srs | yaml ...（singbox 用 binary/source）
	Interval int    // 更新间隔（秒）
}

// mihomoSetURL mihomo 规则集地址（上游文件名为裸名：geo/geosite/cn.mrs）
func mihomoSetURL(dir, name string) string {
	return fmt.Sprintf("https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/%s/%s.mrs", dir, name)
}

// singboxSetURL sing-box 规则集地址（上游文件带前缀：geosite-cn.srs / geoip-cn.srs）
func singboxSetURL(dir, name string) string {
	if dir == "geoip" {
		return "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-" + name + ".srs"
	}
	return "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-" + name + ".srs"
}

// coreHasSet 判断该规则集在目标内核的上游是否存在
func coreHasSet(core string, def ruleSetDef) bool {
	switch def.Cores {
	case "mihomo":
		return core == "mihomo"
	case "singbox":
		return core == "singbox"
	}
	return true
}

func normalizeSets(q genReq) ([]normSet, []string) {
	var warns []string
	// 下载源在这里就定下来：目录规则集与后面 GEOSITE/GEOIP 动态 provider
	// 必须用同一个源，否则同一份配置里一半走镜像一半直连，排障时看不出规律。
	mir := q.mirror()
	seen := map[string]bool{}
	var out []normSet
	add := func(s normSet) {
		if seen[s.Tag] {
			return
		}
		seen[s.Tag] = true
		out = append(out, s)
	}
	// 请求里没带 policy（老版本 UI、直接调 API 的脚本）时回落到目录里的推荐值，
	// 而不是一律 PROXY——否则「国内站点默认走代理」会从另一条路绕回来。
	// 目录是唯一事实源，这里必须真的用它，不能只把它导出给前端。
	policy := func(p, def string) string {
		switch p {
		case "DIRECT", "PROXY", "REJECT":
			return p
		}
		switch def {
		case "DIRECT", "PROXY", "REJECT":
			return def
		}
		return "PROXY"
	}
	for _, s := range q.Sets {
		def, ok := ruleSetIndex[s.ID]
		if !ok {
			warns = append(warns, "未知规则集已跳过: "+s.ID)
			continue
		}
		// 上游未提供该规则集的内核直接跳过（避免生成 404 的远程引用）
		if !coreHasSet(q.Core, def) {
			warns = append(warns, def.Name+" 在 "+q.Core+" 上游没有对应规则集，已跳过")
			continue
		}
		bare := strings.TrimPrefix(strings.TrimPrefix(def.ID, "geosite-"), "geoip-")
		// 目录内置的规则集一律走当前选中的下载源。自定义规则集的 URL 不动：
		// 那是用户自己填的地址，可能本来就在用自家镜像，替人改写只会帮倒忙。
		if q.Core == "mihomo" {
			u, _ := mir.apply(mihomoSetURL(def.Dir, bare))
			add(normSet{Tag: def.ID, URL: u,
				Behavior: def.Behavior, Policy: policy(s.Policy, def.Policy), Format: "mrs", Interval: 86400})
		} else {
			u, _ := mir.apply(singboxSetURL(def.Dir, bare))
			add(normSet{Tag: def.ID, URL: u,
				Behavior: def.Behavior, Policy: policy(s.Policy, def.Policy), Format: "binary", Interval: 86400})
		}
	}
	for _, cs := range q.CustomSets {
		if cs.URL == "" || cs.Name == "" {
			warns = append(warns, "自定义规则集缺少名称或 URL，已跳过: "+cs.Name)
			continue
		}
		beh := cs.Behavior
		if beh != "domain" && beh != "ipcidr" && beh != "classical" {
			beh = "domain"
		}
		interval := cs.Interval
		if interval <= 0 {
			interval = 86400
		}
		tag := "custom-" + strings.ReplaceAll(strings.ReplaceAll(cs.Name, "/", "_"), " ", "_")
		lu := strings.ToLower(cs.URL)
		fmt := "yaml"
		if q.Core == "singbox" {
			if strings.HasSuffix(lu, ".srs") {
				fmt = "binary"
			}
		} else {
			switch {
			case strings.HasSuffix(lu, ".mrs"):
				fmt = "mrs"
			case strings.HasSuffix(lu, ".txt"), strings.HasSuffix(lu, ".list"):
				fmt = "text"
			}
		}
		if q.Core == "singbox" && !strings.HasSuffix(strings.ToLower(cs.URL), ".srs") {
			warns = append(warns, "sing-box 自定义规则集建议使用 .srs 格式: "+cs.Name)
		}
		// 自定义规则集没有目录条目，也就没有推荐值可回落
		add(normSet{Tag: tag, URL: cs.URL, Behavior: beh, Policy: policy(cs.Policy, ""), Format: fmt, Interval: interval})
	}
	// 排序保证稳定输出：REJECT → PROXY → DIRECT
	rank := map[string]int{"REJECT": 0, "PROXY": 1, "DIRECT": 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Policy] < rank[out[j].Policy] })
	return out, warns
}

// ==================== 生成器公共结构 ====================

type generator struct {
	doc    map[string]any
	header string
}

// bypassSpec 是「自动直连节点服务器」生成出来的规则原料。
// 域名与 IP 必须分开：mihomo 用 DOMAIN / IP-CIDR，sing-box 用 domain / ip_cidr，
// 混在一起会让其中一边拿到永远匹配不上的规则（而且内核不报错）。
type bypassSpec struct {
	Domains []string
	CIDRs   []string
}

func (b bypassSpec) empty() bool { return len(b.Domains) == 0 && len(b.CIDRs) == 0 }

// hosts 回显给前端：用户得看见「这次到底直连了哪些地址」，否则一个开关
// 打开后配置里多了几行语法不明的规则，出问题时没法核对。
func (b bypassSpec) hosts() []string {
	out := append([]string{}, b.Domains...)
	return append(out, b.CIDRs...)
}

// normalizeNodeHost 抹掉「公网地址」设置里可能带的端口、IPv6 方括号与尾点。
// publicHost() 是原样返回用户填的值，有人会写 1.2.3.4:443 或 [2001:db8::1]:443；
// 直接拿去生成 DOMAIN / IP-CIDR 就是一条永远匹配不上的规则，而且两种内核
// 都不会报错——症状仍然是「直连规则写了却照样回环」。
func normalizeNodeHost(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	// 只在真的能拆出 host:port 时才剥端口：裸 IPv6（2001:db8::1）里的冒号
	// 不是端口，SplitHostPort 会报错，此时保留原串交给 ParseIP。
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// collectBypassHosts 从导出节点里收集去重的服务器地址。
// 同一个公网地址下会有「入站 × 用户」很多个节点，不去重会生成几十条一样的
// 规则；IPv4 补 /32、IPv6 补 /128，否则 sing-box 的 ip_cidr 不认裸 IP。
func collectBypassHosts(nodes []*link.Node) bypassSpec {
	var b bypassSpec
	seenD, seenC := map[string]bool{}, map[string]bool{}
	for _, n := range nodes {
		h := normalizeNodeHost(n.Host)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			cidr := h + "/32"
			if ip.To4() == nil {
				cidr = h + "/128"
			}
			if !seenC[cidr] {
				seenC[cidr] = true
				b.CIDRs = append(b.CIDRs, cidr)
			}
			continue
		}
		if !seenD[h] {
			seenD[h] = true
			b.Domains = append(b.Domains, h)
		}
	}
	return b
}

// customRule 是一条解析后的自定义规则。
type customRule struct {
	Typ       string   // DOMAIN / DOMAIN-SUFFIX / DOMAIN-KEYWORD / IP-CIDR / IP-CIDR6 / GEOSITE / GEOIP
	Val       string   // 规范化之后的值（可能已补掩码、去掉通配符）
	Pol       string   // DIRECT / PROXY / REJECT
	NoResolve bool     // 仅 IP-CIDR / IP-CIDR6 有意义（clash 的 no-resolve）
	Notes     []string // 对值做了哪些纠正，必须回显给用户
	Reason    string   // 解析失败的具体原因（ok=false 时有效）
}

// parseCustomRuleFull 解析 clash 风格自定义规则：TYPE,VALUE,POLICY[,no-resolve]
// 支持 DOMAIN / DOMAIN-SUFFIX / DOMAIN-KEYWORD / IP-CIDR / IP-CIDR6 / GEOSITE / GEOIP
//
// 生成配置必须用这一个，而不是 parseCustomRule：简化版丢掉 no-resolve，
// 也拿不到「值被纠正了」的说明——用户写的标准 clash 规则会悄悄变意却没有任何提示。
func parseCustomRuleFull(line string) (customRule, bool) {
	var c customRule
	parts := strings.Split(strings.TrimSpace(line), ",")
	// 尾部可选的 no-resolve。clash 的写法是写在策略之后，官方语法就是四段。
	// 之前只认三段，于是 `IP-CIDR,1.2.3.0/24,DIRECT,no-resolve` 被判成策略
	// "NO-RESOLVE"→整条丢弃，用户只看到一行「无法解析已跳过」。
	if len(parts) > 0 {
		if last := strings.ToLower(strings.TrimSpace(parts[len(parts)-1])); last == "no-resolve" {
			c.NoResolve = true
			parts = parts[:len(parts)-1]
		}
	}
	if len(parts) < 3 {
		c.Reason = "至少要写成 类型,值,策略"
		return c, false
	}
	c.Typ = strings.ToUpper(strings.TrimSpace(parts[0]))
	c.Pol = strings.ToUpper(strings.TrimSpace(parts[len(parts)-1]))
	switch c.Pol {
	case "DIRECT", "PROXY", "REJECT":
	default:
		c.Reason = "策略只能是 DIRECT / PROXY / REJECT"
		return c, false
	}
	val := strings.TrimSpace(strings.Join(parts[1:len(parts)-1], ","))

	var errMsg string
	var notes []string
	switch c.Typ {
	case "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD":
		c.Typ, c.Val, notes, errMsg = normalizeDomainValue(c.Typ, val)
	case "IP-CIDR", "IP-CIDR6":
		c.Typ, c.Val, notes, errMsg = normalizeCIDRValue(c.Typ, val)
	case "GEOSITE", "GEOIP":
		if val == "" {
			errMsg = "缺少分类名"
		} else if strings.ContainsAny(val, "/* ") {
			errMsg = "分类名不能带通配符、斜杠或空格"
		} else {
			c.Val = val
		}
	default:
		errMsg = "不支持的类型 " + c.Typ
	}
	if errMsg != "" {
		c.Reason = errMsg
		return c, false
	}
	c.Notes = notes
	// no-resolve 只对 IP 规则有意义。写在域名规则上时，部分内核会直接拒绝解析
	// 整份配置（而不是忽略这一行），所以在这里摘掉并说明，不让它变成启动失败。
	if c.NoResolve && c.Typ != "IP-CIDR" && c.Typ != "IP-CIDR6" {
		c.NoResolve = false
		c.Notes = append(c.Notes, "no-resolve 只对 IP-CIDR 生效，已忽略")
	}
	return c, true
}

// parseCustomRule 是 parseCustomRuleFull 的简化包装，只给「只关心类型和值」
// 的调用方（内置清单断言、清单匹配、DNS 派生）用。
func parseCustomRule(line string) (typ, val, pol string, ok bool) {
	c, ok := parseCustomRuleFull(line)
	return c.Typ, c.Val, c.Pol, ok
}

// describeCustomNotes 把解析阶段的纠正说明转成给用户看的提示。
func describeCustomNotes(line string, notes []string) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, "自定义规则 "+line+"："+n)
	}
	return out
}

// normalizeDomainValue 把域名规则纠正成内核真的能匹配的形式。
//
// 两种写法语法合法、内核不报错、日志里也看不出异常，只有「没效果」：
//
//  1. 前置通配符：DOMAIN-SUFFIX 本身就是按点边界做后缀匹配，`*.foo.com` 的
//     第一个字符是 `*`，它只能匹配叫 `*.foo.com` 的域名——现实中不存在。
//  2. DOMAIN 带通配符：DOMAIN 是完整相等匹配，同上，永远匹配不上。
//
// 这类错误只能靠解析阶段发现，所以在这里纠正并把说明带回去，而不是丢弃。
func normalizeDomainValue(typ, val string) (string, string, []string, string) {
	v := strings.TrimSpace(val)
	if v == "" {
		return "", "", nil, "值为空"
	}
	if strings.ContainsAny(v, "/ :") {
		return "", "", nil, "值应为裸域名，不要带协议、端口或路径"
	}
	var notes []string
	switch typ {
	case "DOMAIN-SUFFIX":
		if t := strings.TrimLeft(v, "*."); t != v {
			if t == "" || strings.Contains(t, "*") {
				return "", "", nil, "通配符位置无法用后缀表达；请写具体域名，或改用 DOMAIN-KEYWORD"
			}
			notes = append(notes, fmt.Sprintf("后缀匹配不需要通配符，已按「%s」处理", t))
			v = t
		}
	case "DOMAIN":
		if strings.Contains(v, "*") {
			t := strings.TrimLeft(v, "*.")
			if t == "" || strings.Contains(t, "*") {
				return "", "", nil, "DOMAIN 只做完整匹配，不能含通配符；请改用 DOMAIN-SUFFIX 或 DOMAIN-KEYWORD"
			}
			notes = append(notes, fmt.Sprintf("DOMAIN 只做完整匹配，已改为 DOMAIN-SUFFIX,%s", t))
			return "DOMAIN-SUFFIX", t, notes, ""
		}
	case "DOMAIN-KEYWORD":
		if strings.Contains(v, "*") {
			t := strings.ReplaceAll(v, "*", "")
			if t == "" {
				return "", "", nil, "关键字不能只有通配符"
			}
			notes = append(notes, fmt.Sprintf("关键字匹配不需要通配符，已按「%s」处理", t))
			v = t
		}
	}
	return typ, v, notes, ""
}

// normalizeCIDRValue 补全缺省的掩码，并拒收写错的地址。
//
// 之前缺掩码的地址只在 sing-box 侧被补成 /32，mihomo 侧原样输出——
// 同一行规则在两种产物里一个能用、一个起不来。
func normalizeCIDRValue(typ, val string) (string, string, []string, string) {
	v := strings.TrimSpace(val)
	if v == "" {
		return "", "", nil, "值为空"
	}
	if strings.Contains(v, "/") {
		if _, _, err := net.ParseCIDR(v); err != nil {
			return "", "", nil, "不是合法的 CIDR（" + v + "）"
		}
		return typ, v, nil, ""
	}
	ip := net.ParseIP(v)
	if ip == nil {
		return "", "", nil, "不是合法的 IP 地址（" + v + "）"
	}
	var notes []string
	if ip.To4() != nil {
		if typ == "IP-CIDR6" {
			notes = append(notes, fmt.Sprintf("%s 是 IPv4 地址，类型已改为 IP-CIDR", v))
			typ = "IP-CIDR"
		}
		notes = append(notes, "缺少掩码，已按 /32（单个地址）处理")
		return typ, v + "/32", notes, ""
	}
	if typ == "IP-CIDR" {
		notes = append(notes, fmt.Sprintf("%s 是 IPv6 地址，类型已改为 IP-CIDR6", v))
		typ = "IP-CIDR6"
	}
	notes = append(notes, "缺少掩码，已按 /128（单个地址）处理")
	return typ, v + "/128", notes, ""
}

// geoProviderFor GEOSITE/GEOIP 自定义规则需要的动态 provider
func geoProviderFor(core, typ, val, pol string, sets *[]normSet, mir ruleMirrorSel) {
	tag := strings.ToLower(typ) + "-" + val
	for _, s := range *sets {
		if s.Tag == tag {
			return
		}
	}
	dir := "geosite"
	if typ == "GEOIP" {
		dir = "geoip"
	}
	var s normSet
	beh := map[string]string{"geosite": "domain", "geoip": "ipcidr"}[dir]
	if core == "mihomo" {
		u, _ := mir.apply(mihomoSetURL(dir, val))
		s = normSet{Tag: tag, URL: u, Behavior: beh, Policy: pol, Format: "mrs", Interval: 86400}
	} else {
		u, _ := mir.apply(singboxSetURL(dir, val))
		s = normSet{Tag: tag, URL: u, Behavior: beh, Policy: pol, Format: "binary", Interval: 86400}
	}
	*sets = append(*sets, s)
}

// privateCIDRs 本机 / 局域网 / 特殊用途网段，一律直连。
//
// 除 RFC1918 外还刻意收了四类最容易漏、症状又最难归因的：
//   - 100.64.0.0/10：CGNAT 段。Tailscale / ZeroTier 的虚拟内网与部分运营商
//     内网都在这里，漏了它打洞流量会被塞进代理隧道，表现为「Tailscale 时通时断」；
//   - 224.0.0.0/4 + ff00::/8：组播。mDNS（隔空投送 / 打印机 / 智能家居）、
//     SSDP（Chromecast / DLNA）全靠组播，漏了它设备发现会时好时坏；
//   - 255.255.255.255/32：DHCP 与有限广播；
//   - fe80::/10 + fc00::/7：IPv6 链路本地与唯一本地地址。
//
// 这些网段直连不会有任何副作用：它们本来就不可能出现在公网代理出口上。
var privateCIDRs = []string{
	"192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12",
	"127.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10",
	"224.0.0.0/4", "255.255.255.255/32",
	"::1/128", "fc00::/7", "fe80::/10", "ff00::/8",
}

// localOnlySuffixes 是「公网 DNS 一定答不出来、必须由本地解析器回答」的名字后缀。
//
// 这些是标准规定的特殊用途域名，不是谁家的服务清单：
//   - local（RFC 6761）：mDNS，隔空投送 / 打印机 / 智能家居发现；
//   - home.arpa（RFC 8375）：家庭网络专用域；
//   - lan / localdomain：dnsmasq 与大量家用路由的默认域，实测广泛在用。
//
// 两套内核都需要这件事，但机制完全不同，所以各写一遍时最容易漏一边：
// mihomo 是 fake-ip 模式下把域名排除出 fake-ip（否则拿到 198.18.x.x，局域网
// 名字当然连不上）；sing-box 没有 fake-ip，必须显式写成 `dns.rules → local`，
// 否则落到 `final: remote`，经代理去问 DoH——局域网主机名必然解析失败。
// 此前只有 mihomo 侧有 `*.lan` / `*.local`，sing-box 用户的 NAS、打印机
// 主机名一直解析不了，而两边配置都合法、都不报错。
var localOnlySuffixes = []string{"lan", "local", "home.arpa", "localdomain"}

// lanDNSNotice 返回「这个组合会坏事」的提醒，没事可说时返回空串。
//
// 空值**不**进警告。理由不是「不重要」，而是警告在这套界面里等于「这次生成有
// 东西要你处理」：它既留在页面上，又弹一条 toast，还会顶掉「生成完成」。把一条
// 几乎人人可忽略的提示塞进去，结果是所有警告一起被学会无视——那比不提示更糟。
// 「留空意味着什么」写在生成页那个输入框的说明里（就在手边），边界写进
// docs/known-limits.md。
//
// 这里只剩一种情况真该拦一下：选了系统解析器、而这份配置自己在管 :53。
// 那时若系统解析器指向本机，查询就绕回自己——一个名字都解析不出来，且症状
// （全屋断网）与「填对了但服务挂了」完全一样，事后极难归因。
func lanDNSNotice(lan lanResolver, router bool) string {
	if lan.isSystem() && router {
		return "「局域网 DNS」选的是系统解析器，而这份配置自己也在接管 :53——" +
			"如果系统的解析器就指向本机（/etc/resolv.conf 里是 127.0.0.1），查询会绕回自己形成 DNS 环，" +
			"一个名字都解析不出来。不确定系统解析器指向哪里的话，改填路由器地址。"
	}
	return ""
}

// fakeIPFilter 只收「fake-ip 本身会坏事、而 sing-box 侧不存在对应问题」的条目：
// STUN / 主机待机联网（Nintendo、PlayStation、Xbox）拿假 IP 就没法工作，
// 时间同步域名拿到假 IP 会让 NTP 客户端连到 198.18.x.x。
//
// 连通性检测（msftconnecttest / msftncsi）不在这里——它们属于内置直连类，
// 由 systemDirectFakeIPFilter 派生，写两遍只会让后来的人以为要一起改。
var fakeIPFilter = []string{
	"+.stun.*", "+.srv.nintendo.net", "+.stun.playstation.net",
	"xbox.*.microsoft.com", "+.xboxlive.com", "time.*.com", "ntp.*.com",
}

// blockExemptRules 是「即使命中了拦截类规则集，也必须直连」的域名。
//
// 与 systemDirectRules 的失效模式不同：那份是「走代理会坏」，这份是「被面板
// 自己勾的拦截规则掐死会坏」。所以它**不进** fake-ip-filter、**不进** DNS 规则
// 派生——豁免只影响路由判定，不影响解析。
//
// ── 为什么需要这份清单 ──
// 「广告域名（通用全量）」geosite-category-ads-all 是上游的大杂烩分类：除真广告
// 之外，它还收了国内 App 的日志上报域名。这些埋点一旦被 REJECT（直接 RST），
// 不少 App 的埋点 SDK 会在冷启动阶段同步等首屏事件落盘，失败就退避重试——现象
// 是**首屏一直转圈**，而 API、图片、SSO 全都正常，查分流永远查不出来。
//
// 2026-11-23 实测（OpenWrt 旁路由 + mihomo 1.19.34）：红果短剧首屏卡死，45 秒
// 日志里 log5-applog.fqnovel.com 被拒 12 次，rtlog3/rtlog5-applog 同样密集重试；
// 放行后首屏当场通过。同刻 api5-normal.fqnovel.com、封面 CDN、snssdk 系列全部
// geosite-cn 直连且有 1.4 MB 下行——网络侧一直是通的，唯独埋点被掐。
//
// ── 准入判据（新增条目必须同时满足）──
//  1. 属于应用自身的日志 / 埋点上报，不是第三方广告投放；
//  2. 被 REJECT 会阻塞应用主流程，而不是「少一条上报」。仅凭「可能慢」不收；
//  3. 写法必须覆盖同族变体——字节的埋点主机名是 log{N}-applog /
//     rtlog{N}-applog，序号随时会变，逐条列举必然漏。
//
// ── 什么时候整份不生效 ──
// 用户显式勾了「遥测 / 数据上报」这类**本身就是拦上报**的规则集并设为 REJECT 时，
// 一条都不插（见 blockExemptDecision）：「隐私优先」「纯净拦截」预设就是这种意图，
// 那边放行埋点等于违背用户的选择。但「不插」不等于「不说」——那种配置下 App 首屏
// 会卡，所以照样给提示，见 explicitBlockWarn。
var blockExemptRules = []string{
	// 字节系应用日志上报。注意 log5-applog 里的 log5-applog 是**一个 DNS
	// 标签**，连字符不是分隔符，所以 DOMAIN-SUFFIX,applog.fqnovel.com 永远
	// 匹配不到它（这正是当初「加了规则却没生效」的原因），只能用 KEYWORD。
	"DOMAIN-KEYWORD,applog,DIRECT",
	// 字节自有接口域。实测 log.zijieapi.com 命中 ads-all 被拒；同域下还有
	// mon / dig 等日志与配置接口，一并放行。该域是字节自己的 API 入口，
	// 不含第三方广告联盟，不存在「放行了就漏广告」的问题。
	"DOMAIN-SUFFIX,zijieapi.com,DIRECT",
}

// blockExemptWarn 插入豁免时给用户的说明。前端会常驻显示，所以写清「为什么」
// 和「不想要时怎么办」，而不是一句「已自动处理」。
const blockExemptWarn = "广告拦截已放行应用日志域名（applog / zijieapi.com）：" +
	"拦掉它们会让部分 App 首屏卡死，而它们不是广告内容。" +
	"要连埋点一起拦，请改用「遥测 / 数据上报」规则集；" +
	"另有 App 卡首屏时，把它上报用的日志域名加成 DOMAIN-KEYWORD 自定义规则即可。"

// explicitBlockWarn 是「用户明确要拦上报」时不插豁免的说明。
//
// 这段提示存在的理由：不插豁免是正确决定，但**用户看不到后果**。他勾的是
// 「遥测 / 数据上报」，看到的是「配置生成成功」，第二天发现红果卡在首屏，
// 只会怀疑分流或节点。配置合法、内核不报错、日志干净——又是这个仓库最怕的形状。
func explicitBlockWarn(setName string) string {
	return "你勾了「" + setName + "」并设为拒绝：应用自己的日志 / 埋点上报也在里面。" +
		"被拒后部分 App 的埋点 SDK 会一直重试，表现是首屏卡住不动（我实测红果短剧如此）。" +
		"这是你明确要的效果，所以没有自动放行；想恢复正常，把它改成「走代理」或取消勾选。"
}

// blockExemptPlan 是「这次生成要不要插豁免、要不要给提示」的结论。
type blockExemptPlan struct {
	Insert bool   // 插 blockExemptRules
	Warn   string // 常驻提示；空 = 不提示
}

// blockExemptDecision 判断本次生成要不要插拦截豁免。
//
// 判据来自规则集目录里的 AppLog 字段，而不是在这里列举集合 ID：
// 「广告域名（精简）」上游同样带着 log.zijieapi.com，按 ID 列举时它被漏在外面，
// 选了它照样 RST、豁免一条不插——和当初红果那次一模一样，只是换了个勾选框。
// 现在新增任何广告类集合，只需在目录里填 AppLog 一行，这个函数不用改。
func blockExemptDecision(sets []normSet) blockExemptPlan {
	var mixed, explicit string
	for _, s := range sets {
		if s.Policy != "REJECT" {
			continue // 没设成拒绝就没有误杀可言
		}
		def, ok := ruleSetIndex[s.Tag]
		if !ok {
			continue // 自定义规则集没有目录条目，无从判断
		}
		switch def.AppLog {
		case appLogMixed:
			if mixed == "" {
				mixed = def.Name
			}
		case appLogExplicit:
			if explicit == "" {
				explicit = def.Name
			}
		}
	}
	switch {
	case explicit != "":
		// 明确要拦上报：把选择权留给用户，但后果要说清。
		return blockExemptPlan{Warn: explicitBlockWarn(explicit)}
	case mixed != "":
		return blockExemptPlan{Insert: true, Warn: blockExemptWarn}
	}
	return blockExemptPlan{}
}

// blockExemptNeeded 只关心「插不插」的调用方（测试）用这个。
func blockExemptNeeded(sets []normSet) bool { return blockExemptDecision(sets).Insert }

// systemDirectPorts 是「按端口判断、拿不到域名」的必须直连流量。
//
// 目前只有 NTP：它通常是客户端直接向 IP 发 UDP 123，没有 SNI、没有域名，
// 任何域名规则都匹配不到，于是落进 MATCH,PROXY。而代理节点几乎都不转发
// UDP 123 → 系统时间慢慢漂移 → **TLS 证书校验开始随机失败**，症状是
// 「浏览器时不时报证书错误 / 证书过期」，极难归因到代理上。
// 时间同步必须走本地网络，这里按端口兜底，不依赖域名嗅探。
var systemDirectPorts = []int{123}

// systemProxyCIDRs 与 github 类的域名规则配套：域名部分覆盖 HTTPS（按 SNI 嗅探
// 出的域名匹配），这些 IP 段覆盖**没有域名**的裸 TCP，主要是 git over SSH——
// SSH 走 22 端口、没有 SNI，域名规则对它完全无效，只能按目标 IP 匹配。
//
// 段取自 GitHub 官方 https://api.github.com/meta 的 git 列表，只保留 GitHub 自有 /
// 独占段，不取大块 Azure 段（会误伤同机房服务）。上游那份列表现在是 111 条，这里
// 只收常用的几条——漏一条的后果是该连接落回 MATCH,PROXY，方向与这些规则一致，
// 所以不必把 111 条全抄进来，更不必为「新出现的段」改动代码。
var systemProxyCIDRs = []string{
	"192.30.252.0/22",   // GitHub 传统段（web/git/api/pages/hooks 都有）
	"185.199.108.0/22",  // Pages / raw / githubassets 的 CDN 段
	"140.82.112.0/20",   // git 主力段，SSH 最常落在这里
	"143.55.64.0/20",    // git 备用段
	"20.6.7.16/28",      // 实测：github.com 当前解析到 20.6.7.17
	"20.205.243.160/28", // meta 里的 api/git 端点（.161 / .166 / .168）
	"57.155.223.240/28", // 实测：codeload.github.com 解析到 57.155.223.241
}

// sbRuleBody 把 clash 风格的规则类型/值转成 sing-box 的规则匹配体（不含 outbound）。
//
// 抽出来是为了**只有一处**知道「哪种类型怎么表达」：自定义规则与两份内置清单
// 共用它。此前内置清单各自写了 switch，只认 DOMAIN / DOMAIN-SUFFIX，
// 于是往清单里加一条 DOMAIN-KEYWORD 时 mihomo 生效、sing-box 静默不生效——
// 内核不报错、配置也合法，是最难查的一类缺陷。返回 nil 表示确实无法表达，
// 调用方必须告警而不是当作没事（见 warnUnsupportedRule）。
func sbRuleBody(typ, val string) map[string]any {
	switch typ {
	case "DOMAIN":
		return map[string]any{"domain": []any{val}}
	case "DOMAIN-SUFFIX":
		return map[string]any{"domain_suffix": []any{val}}
	case "DOMAIN-KEYWORD":
		return map[string]any{"domain_keyword": []any{val}}
	case "IP-CIDR", "IP-CIDR6":
		if !strings.Contains(val, "/") {
			val += map[bool]string{true: "/128", false: "/32"}[strings.Contains(val, ":")]
		}
		return map[string]any{"ip_cidr": []any{val}}
	}
	return nil
}

// systemDirectDNSForms 把 systemDirectRules 拆成「精确域名 / 后缀」两组。
//
// 抽出来的理由与 sbRuleBody 相同：mihomo 的 `fake-ip-filter` 与 sing-box 的
// `dns.rules → local` 是同一件事的两种写法（这些域名必须拿真实 IP、必须本地
// 解析，否则「DNS 经代理、流量走直连」形成死锁）。两边各自写一遍 parse+switch
// 时，一边加了新类型、另一边静默漏掉——两边都不报错，只有那个服务连不上。
//
// 只认 DOMAIN 与 DOMAIN-SUFFIX：IP-CIDR 之类与域名过滤无关，写进 fake-ip-filter
// 不报错但毫无作用；清单允许的类型由 TestSystemListsUseSupportedRuleTypes 钉住。
func systemDirectDNSForms() (exact, suffix []string) {
	exact = make([]string, 0, len(systemDirectRules))
	suffix = make([]string, 0, 8)
	for _, line := range systemDirectRules {
		typ, val, _, ok := parseCustomRule(line)
		if !ok {
			continue // 清单是我们自己写的，解析失败只能是代码 bug，由单测兜住
		}
		switch typ {
		case "DOMAIN":
			exact = append(exact, val)
		case "DOMAIN-SUFFIX":
			suffix = append(suffix, val)
		}
	}
	return exact, suffix
}

// systemDirectFakeIPFilter 是 systemDirectRules 在 mihomo fake-ip-filter 里的对应写法。
// TUN + fake-ip 下若不排除，这些域名会拿到 198.18.x.x 假 IP，依赖证书固定的
// 客户端（App Store / OneDrive / Microsoft 账号）握手失败——规则再对也连不上。
// 与 sing-box 的 dns.rules 同源派生（systemDirectDNSForms），避免两套产物漂移。
func systemDirectFakeIPFilter() []string {
	exact, suffix := systemDirectDNSForms()
	out := make([]string, 0, len(exact)+len(suffix)+len(localOnlySuffixes))
	out = append(out, exact...)
	for _, s := range suffix {
		out = append(out, "+."+s)
	}
	// 局域网专用后缀同样不能进 fake-ip：`nas.lan` 拿到 198.18.x.x 就再也连不上。
	for _, s := range localOnlySuffixes {
		out = append(out, "+."+s)
	}
	return out
}

// generatorStamp 是对「当前生成器行为」取的指纹：用一份固定的最小输入跑一遍
// mihomo 生成器，对**产物本身**做哈希。
//
// 为什么不是再往快照里加几行（嗅探端口、MTU、规则位置……）：那还是枚举，
// 下次改的是没被枚举到的旋钮时，用户照样收不到「该重新生成」的提示。v2.1.5
// 就是现成的例子——真正让 APNs 规则生效的是嗅探端口从 `443,8443` 放开到
// 全端口，而旧快照只记规则行，那一版之所以还报了漂移，纯粹因为同时新增了
// `push.apple.com` 这一行。哪天只改嗅探，漂移就是零条、界面显示「一致」。
//
// 取产物指纹等于把判据换成「生成器还吐不吐同样的东西」，新增任何影响产物的
// 开关都不用记得来这里登记。刻意排除头注释：里面带版本号与下载源，会让每次
// 发版都报漂移，而那种漂移没有行为含义。
//
// 输入固定为「路由目标 + 零节点 + 零规则集 + 零自定义规则 + 官方源」：
// 规则清单、嗅探、TUN、DNS、分组结构都在这条路径上，且不依赖任何用户数据。
func generatorStamp() string {
	var warns []string
	g := genMihomo("router", nil, nil, nil, &warns, genOpts{})
	return stampOf(g.doc)
}

// stampOf 对一份产物取短指纹。抽出来是为了让测试能证明「产物变了 → 指纹变了」，
// 而不是只证明「同一份产物两次哈希一样」。
func stampOf(doc map[string]any) string {
	body, err := yaml.Marshal(doc)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])[:12]
}

// mihomoMixedPort 客户端配置里的默认混合端口（HTTP/SOCKS 同端口）。
// 注意这只是「文件里写的值」：Clash Verge 默认 7897、FlClash 也有自己的端口设置，
// 客户端会用自身设置覆盖它，所以头注释必须提醒，见 genMihomo。
const mihomoMixedPort = 7890

// targetNotes 各目标的使用说明（YAML 头注释 / UI 提示）
func targetNote(target string) string {
	switch target {
	case "main-router":
		return "主路由模式：TUN 接管全机流量，DNS 劫持 :53，局域网设备无需任何配置。\n重启路由前建议先在单设备上验证配置。"
	case "side-router":
		return "旁路由模式：将本机 IP 设为局域网设备的网关与 DNS 即可接管；\n主路由无需改动（关闭主路由 DHCP 冲突即可并存）。"
	case genTargetMomo:
		return "OpenWrt-momo：输出完整的 sing-box config.json 作为 profile，自带 momo 固定 tag 的入站与 fake-ip DNS；\n" +
			"momo 自己接管 nftables / 策略路由，配置里的 tun-in 必须保持 auto_route:false。"
	default:
		return "客户端模式：默认使用混合端口 7890（HTTP/SOCKS），配合系统代理或浏览器插件使用。"
	}
}

// ==================== mihomo 生成器 ====================

func genMihomo(target string, nodes []*link.Node, sets []normSet, customRules []string, warns *[]string, opts genOpts) *generator {
	router := target != "client"
	mir, bypass, lan := opts.Mirror, opts.Bypass, opts.LAN

	proxies := []any{}
	names := []string{}
	for _, n := range nodes {
		if p, err := link.ToClash(n); err == nil {
			proxies = append(proxies, p)
			names = append(names, p["name"].(string))
		}
	}

	// 规则：节点服务器直连 → 自定义 → 规则集 → 私网 → MATCH
	// （normalizeSets 已按 REJECT>PROXY>DIRECT 排序）
	// 节点服务器那几条**必须排在最前**：下面任何一条（自定义 REJECT、境外规则集
	// PROXY、末尾的 MATCH,PROXY）都可能把它吸进隧道，形成「用隧道去连隧道
	// 端点」的回环。放在私网段里也不行——公网 IP 根本不在私网段内。
	rules := []any{}
	for _, d := range bypass.Domains {
		rules = append(rules, fmt.Sprintf("DOMAIN,%s,DIRECT", d))
	}
	for _, c := range bypass.CIDRs {
		rules = append(rules, fmt.Sprintf("IP-CIDR,%s,DIRECT,no-resolve", c))
	}
	for _, line := range customRules {
		c, ok := parseCustomRuleFull(line)
		if !ok {
			msg := "无法解析的自定义规则已跳过: " + line
			if c.Reason != "" {
				msg += "（" + c.Reason + "）"
			}
			*warns = append(*warns, msg)
			continue
		}
		*warns = append(*warns, describeCustomNotes(line, c.Notes)...)
		if c.Typ == "GEOSITE" || c.Typ == "GEOIP" {
			geoProviderFor("mihomo", c.Typ, c.Val, c.Pol, &sets, mir)
			rules = append(rules, fmt.Sprintf("RULE-SET,%s-%s,%s", strings.ToLower(c.Typ), c.Val, c.Pol))
			continue
		}
		rule := fmt.Sprintf("%s,%s,%s", c.Typ, c.Val, c.Pol)
		// no-resolve 必须原样写回去：不带它时 mihomo 会为了匹配 IP 规则去解析
		// 目标域名，fake-ip 环境下解析出来的是 198.18.x.x，规则反而更匹配不上。
		if c.NoResolve {
			rule += ",no-resolve"
		}
		rules = append(rules, rule)
	}
	// 广告拦截豁免：必须排在**所有规则集之前**——ads-all 的 RULE-SET 一旦先
	// 命中，这些应用日志域名就被 RST 了，App 首屏卡死，而配置看起来毫无问题。
	// 排在自定义规则之后：用户仍可用自定义 REJECT 主动覆盖（那是他的显式选择）。
	// 不插也要说：显式拦遥测时 plan.Warn 会给出一句「首屏会卡」的说明。
	exempt := blockExemptDecision(sets)
	if exempt.Insert {
		for _, line := range blockExemptRules {
			typ, val, pol, ok := parseCustomRule(line)
			if !ok {
				continue // 清单是我们自己写的，解析失败只可能是代码 bug
			}
			rules = append(rules, fmt.Sprintf("%s,%s,%s", typ, val, pol))
		}
	}
	if exempt.Warn != "" {
		*warns = append(*warns, exempt.Warn)
	}
	// 系统关键服务强制直连：必须在所有规则集之前——geosite-microsoft / onedrive
	// 被设成 PROXY 时，若不前置，登录域名会先被 PROXY 规则吃掉（用户反馈的
	// OneDrive 登录不上）。放在自定义规则之后，保留用户显式覆盖的能力。
	for _, line := range systemDirectRules {
		typ, val, pol, ok := parseCustomRule(line)
		if !ok {
			continue // 内置清单是我们自己写的，解析失败只可能是代码 bug，跳过不污染配置
		}
		rules = append(rules, fmt.Sprintf("%s,%s,%s", typ, val, pol))
	}
	// 境外关键服务强制代理（GitHub 等）：同样必须在规则集之前——用户把
	// GitHub 相关规则集设成 DIRECT 时，不能让连接被留在大陆网络上。
	for _, line := range systemProxyDomains {
		typ, val, pol, ok := parseCustomRule(line)
		if !ok {
			continue
		}
		rules = append(rules, fmt.Sprintf("%s,%s,%s", typ, val, pol))
	}
	// NTP：UDP 123 没有域名、没有 SNI，域名规则永远匹配不到，会落进 MATCH,PROXY。
	// 代理节点基本不转发 UDP 123，时间一漂 TLS 证书就开始随机报错，且没人会想到是代理。
	for _, p := range systemDirectPorts {
		rules = append(rules, fmt.Sprintf("DST-PORT,%d,DIRECT", p))
	}
	for _, s := range sets {
		rules = append(rules, fmt.Sprintf("RULE-SET,%s,%s", s.Tag, s.Policy))
	}
	// 裸 TCP 兜底：git over SSH 走 22 端口、没有 SNI，上面的域名规则对它无效。
	// 放在规则集之后——有域名的连接早已按域名命中，这里只接无域名的 IP 连接。
	for _, c := range systemProxyCIDRs {
		rules = append(rules, fmt.Sprintf("IP-CIDR,%s,PROXY,no-resolve", c))
	}
	// 这里刻意**不**加内建 GEOIP,CN 兜底，原因写在这里防止以后有人再想到这条捷径。
	//
	// geoip-cn 是远程规则集：首次启动没有本地缓存、镜像又拉不到时，mihomo 只写一条
	// 日志就把整条 RULE-SET 跳过，然后一路落到 MATCH,PROXY——「国内站点莫名变慢、
	// 个别 App 首屏卡死」，而配置与日志都干净。看起来补一条内建 GEOIP,CN 就兜住了，
	// 但实测（mihomo v1.19.34，home 目录不可写）它在**解析配置阶段**直接退出：
	//
	//	level=fatal msg="Parse config error: rules[0] [GEOIP,CN,DIRECT] error:
	//	can't download MMDB: open <home>/geoip.metadb: permission denied"
	//
	// 即：MMDB 缺失且下载不了（大陆访问 GitHub 的常态）时，「变慢」会变成「内核
	// 起不来」。风险远大于收益。这个口子的兜底只能是可观测性——rule-provider 的
	// `path` 已有本地缓存，体检脚本用 fake-ip 判据能直接发现「国内域名被代理」。
	for _, c := range privateCIDRs {
		rules = append(rules, fmt.Sprintf("IP-CIDR,%s,DIRECT,no-resolve", c))
	}
	rules = append(rules, "MATCH,PROXY")

	providers := map[string]any{}
	for _, s := range sets {
		providers[s.Tag] = map[string]any{
			"type":     "http",
			"behavior": s.Behavior,
			"format":   s.Format,
			"url":      s.URL,
			"path":     "./rules/" + s.Tag + ".mrs",
			"interval": s.Interval,
			// 规则集必须直连下载，不能走代理节点。
			//
			// 不写这个字段时 mihomo 会按 rules 匹配出站，而生成的规则最后一条是
			// MATCH,PROXY——于是「下载规则集」本身要经过节点。首启时节点还没就绪
			// （或节点本来就坏）就会先失败一轮：日志里是
			// `initial rule provider xxx error: EOF`，用户看到的是「配置没错但
			// 分流失效」。选镜像的动机恰恰是「不依赖节点也能把规则集拿到」，
			// 走节点就把这个前提抵消了。
			//
			// 与 sing-box 生成器的 download_detour: "direct" 对齐（那边一直是对的）。
			"proxy": "DIRECT",
		}
	}

	groups := []any{
		map[string]any{"name": "PROXY", "type": "select",
			"proxies":     append([]any{"Auto"}, toAnySlice(names)...),
			"disable-udp": false},
		map[string]any{"name": "Auto", "type": "url-test",
			"proxies": toAnySlice(names), "url": "http://www.gstatic.com/generate_204",
			"interval": 300, "tolerance": 50},
	}

	tun := map[string]any{"enable": false}
	if router {
		tun = map[string]any{
			// 必须显式写 mtu：mihomo 默认 9000，在真实链路（1500）上会让
			// MSS 远超路径 MTU，大包被丢弃又收不到 ICMP 分片通知——现象就是
			// 「网页能开、git push / 大文件下载卡死」。1500 是标准以太网值；
			// 若线路是 PPPoE（1492）仍偶发大包卡顿，可降到 1420。
			"mtu":    1500,
			"enable": true, "stack": "mixed",
			"auto-route": true, "auto-redirect": true,
			"auto-detect-interface": true,
			"dns-hijack":            []any{"any:53"},
		}
	}

	// TUN + fake-ip 下，节点域名也会被映射成 198.18.x.x。把它排除掉，代理
	// 客户端拿到的是真实 IP，直连规则与连接目标才对得上。先复制一份：
	// 直接 append 到全局 fakeIPFilter 的别名上会污染后续所有配置。
	filter := append([]string{}, fakeIPFilter...)
	filter = append(filter, systemDirectFakeIPFilter()...)
	filter = append(filter, bypass.Domains...)
	dns := map[string]any{
		"enable":             true,
		"ipv6":               false,
		"enhanced-mode":      map[bool]string{true: "fake-ip", false: "redir-host"}[router],
		"fake-ip-range":      "198.18.0.1/16",
		"fake-ip-filter":     toAnySlice(filter),
		"nameserver":         toAnySlice([]string{"https://223.5.5.5/dns-query", "https://doh.pub/dns-query"}),
		"default-nameserver": toAnySlice([]string{"223.5.5.5", "119.29.29.29"}),
	}
	if !lan.empty() {
		// nameserver-policy 与 fake-ip-filter 是同一件事的两半，缺一半都不成立：
		//   - 只有 filter：names 拿到真实 IP，但拿去问公网 DoH，回 NXDOMAIN；
		//   - 只有 policy：名字问对了地方，却先把 198.18.x.x 返给客户端，连不上。
		//
		// 四个后缀都由 localOnlySuffixes 派生，不在这里再写一遍清单。
		policy := map[string]any{}
		for _, s := range localOnlySuffixes {
			policy["+."+s] = lan.mihomoValue()
		}
		dns["nameserver-policy"] = policy
	}
	if router {
		dns["listen"] = "0.0.0.0:53"
	}
	if notice := lanDNSNotice(lan, router); notice != "" {
		*warns = append(*warns, notice)
	}

	doc := map[string]any{
		"mode":                "rule",
		"log-level":           map[bool]string{true: "info", false: "warning"}[router],
		"allow-lan":           router,
		"bind-address":        "*",
		"mixed-port":          mihomoMixedPort,
		"external-controller": "127.0.0.1:9090",
		"unified-delay":       true,
		"tcp-concurrent":      true,
		"tun":                 tun,
		"dns":                 dns,
		"proxies":             proxies,
		"proxy-groups":        groups,
		"rule-providers":      providers,
		"rules":               rules,
	}
	if router {
		// QUIC 必须一起嗅：HTTP/3 连的是 UDP:443，只嗅 TLS 拿不到 SNI。
		// 字节系 App（红果、抖音）大量用内置 HTTPDNS + 自研协议，它们连的是
		// 真实 IP 而不是域名——本来就绕过了域名规则；再叠加「QUIC 不嗅」，
		// 这条连接上就一个域名信息都不剩，只能靠 IP 归属判定。
		// 旧版 mihomo 不认 QUIC 键时按未知字段忽略，不会拒收整份配置。
		// ── 为什么 TLS 必须是「全端口」而不是一份端口清单 ──
		//
		// 系统直连清单里的域名全部被派生进 fake-ip-filter，DNS 因此返回真实 IP。
		// 连接进到内核时 `metadata.Host` 是空的，只剩 `DstIP`；域名规则
		// （`DOMAIN` / `DOMAIN-SUFFIX`）走 `metadata.RuleHost()`，此时唯一的
		// 来源就是 `SniffHost`——也就是 SNI。
		//
		// 而 mihomo 的嗅探有硬门槛：`SupportPort(metadata.DstPort)` 为假就直接
		// return false（component/sniffer/dispatcher.go:134），且 ports 缺省是
		// 空集＝不嗅。于是「这条规则能不能生效」变成「这个服务碰巧用了我枚举过
		// 的端口没有」——而端口由第三方决定，我们无权假设。
		//
		// 2026-11-24 实测：APNs 走 5223，枚举里只有 443/8443，`push.apple.com`
		// 的直连规则**完全没生效**，推送长连接照样挂在海外节点上（苹果设备待机
		// 耗电加快）。配置合法、内核不报错、日志干净，只有翻连接列表才看得出来。
		// 同一形状还会在每一个「非 443 的系统服务」上重演：iMessage / FaceTime
		// （5223）、APNs 备用（2197）、邮件（993/143）、目录服务……逐个补端口
		// 是永远补不完的。
		//
		// 所以这里不枚举：加密业务用哪个端口不可预测，只要协议是 TLS 就该能识别。
		// 代价可以忽略——嗅探只对 `Host` 为空的连接触发（见 shouldOverride），
		// 且失败结果进 LRU（5 次后跳过），不影响已有域名信息的流量。
		//
		// 明文 HTTP 与 QUIC 仍按端口收窄：前者在现代系统服务里几乎绝迹，后者的
		// UDP 嗅探要跨包缓存，全端口会把游戏/语音一起卷进来，而 QUIC 端口本就
		// 高度集中在 443。这个取舍写在 docs/known-limits.md（KL-12）。
		doc["sniffer"] = map[string]any{
			"enable": true, "parse-pure-ip": true, "override-destination": false,
			"sniff": map[string]any{
				"HTTP": map[string]any{"ports": []any{80, "8080-8880"}},
				"TLS":  map[string]any{"ports": []any{"1-65535"}},
				"QUIC": map[string]any{"ports": []any{443, 8443}},
			},
		}
	}

	note := strings.ReplaceAll(targetNote(target), "\n", "\n# ")
	header := "# Triune Panel 生成 | mihomo | " + target + "\n# " + note + "\n"
	// 端口是最容易踩的一环：用户照着文件头的 7890 去配浏览器/系统代理，
	// 而 Clash Verge 之类会用它自己设置里的端口（默认 7897），于是连到一个
	// 没人监听的端口上——现象就是「节点好好的，网页全打不开」。
	if !router {
		header += fmt.Sprintf("# 端口提示：混合端口 %d 只是本文件的默认值。\n", mihomoMixedPort) +
			"# Clash Verge / FlClash 等客户端会改用它自己设置里的端口（Clash Verge 默认 7897），\n" +
			"# 配置浏览器或系统代理时，以客户端界面上显示的端口为准。\n"
	}
	// 规则集地址是配置里最容易「看着对但拉不到」的一环，把实际用的下载源
	// 写在头注释里，出问题时一眼能看出是不是镜像挂了。
	if !mir.direct() {
		header += "# 规则集下载源：" + mir.label() + "\n"
	}
	// 规则集是 mrs 二进制格式。旧内核不会「降级按文本读」，而是直接报 format
	// 不支持——用户看到的是「配置导入成功，但分流失效或内核起不来」，而配置
	// 本身每一行都没错。这个下限必须写在文件里，不能只留在更新日志中。
	if len(sets) > 0 {
		header += "# 规则集为 mrs 二进制格式，需要 mihomo 1.18 或更高；旧版会报 format 不支持。\n"
	}
	if !bypass.empty() {
		header += "# 节点服务器直连（避免回环）：" + strings.Join(bypass.hosts(), "、") + "\n"
	}
	// 写进文件头：查「nas.lan 解析不了」时第一眼就能确认这份配置到底填了没有，
	// 不用在几百行 YAML 里翻 nameserver-policy（它只在填过时才存在）。
	if !lan.empty() {
		header += "# 局域网 DNS（.lan/.local/.home.arpa/.localdomain）：" + lan.label() + "\n"
	}
	header += "\n"
	return &generator{doc: doc, header: header}
}

// ==================== sing-box 生成器 ====================

// sbDNSServers DNS 服务器定义。1.12 起是 type+server 的新格式，
// 1.11 只认 address 字符串；写错一边就是启动 FATAL，所以单独抽出来。
//
// 两代格式的 detour 语义不同，不能照抄：
//
//   - 旧格式（1.11）：DNS server 不写 detour 时走 default outbound，
//     也就是 route.final（本模板是 PROXY）——本地 DNS 会被绕进代理。
//     所以必须显式写 detour: "direct"。
//   - 新格式（1.12+）：DNS server 自己就是一个 dialer，官方文档写明
//     "uses dialer just like outbound, which is equivalent to using an
//     empty direct outbound by default"。不写 detour 就是直连；此时再写
//     detour: "direct" 反而会被启动校验拒绝：
//     FATAL start service: start dns/udp[local]:
//     detour to an empty direct outbound makes no sense
//     （SagerNet/sing-box#2803，1.12.0 起）
//
// 注意这条只在 start service 阶段报，`sing-box check` 查不出来——所以
// 用 check 验过的产物仍然可能在客户端启动时炸。
func sbDNSServers(caps sbCaps, lan lanResolver) []any {
	servers := []any{
		map[string]any{"type": "https", "tag": "remote", "server": "8.8.8.8", "detour": "PROXY"},
		map[string]any{"type": "udp", "tag": "local", "server": "223.5.5.5"},
	}
	if caps.NewDNSFormat {
		if !lan.empty() {
			servers = append(servers, sbLANServer(caps, lan))
		}
		return servers
	}
	servers = []any{
		map[string]any{"tag": "remote", "address": "https://8.8.8.8/dns-query", "detour": "PROXY"},
		map[string]any{"tag": "local", "address": "223.5.5.5", "detour": "direct"},
	}
	if !lan.empty() {
		servers = append(servers, sbLANServer(caps, lan))
	}
	return servers
}

// sbLANServer 生成「局域网解析器」那一项。
//
// 注意 `local`（上面那个 223.5.5.5）不是系统解析器——它的 tag 叫 local，但它是
// 一个公网 UDP DNS。这也是为什么单靠 v2.1.7 给 sing-box 补 dns.rules → local
// 并不能让 nas.lan 解析成功：指向的目标本身就答不出局域网名字。
//
// 旧格式（1.11）必须写 detour: direct。不写时 DNS 查询走 route.final，也就是
// PROXY——把「查局域网 DNS」的包送进隧道，那台服务器在隧道另一头根本不可达，
// 而且失败信息只会是超时。
func sbLANServer(caps sbCaps, lan lanResolver) map[string]any {
	host, port := lan.hostPort()
	if caps.NewDNSFormat {
		if lan.isSystem() {
			return map[string]any{"type": "local", "tag": "lan"}
		}
		m := map[string]any{"type": "udp", "tag": "lan", "server": host}
		if port != 53 {
			m["server_port"] = port
		}
		return m
	}
	addr := host
	if lan.isSystem() {
		addr = "local"
	} else if port != 53 || strings.Contains(host, ":") {
		addr = net.JoinHostPort(host, strconv.Itoa(port))
	}
	return map[string]any{"tag": "lan", "address": addr, "detour": "direct"}
}

// sbDirectHTTPClientTag 是「规则集下载走直连」这个 HTTP 客户端的标签。
// sing-box 1.14+ 用 http_client 引用它，取代已废弃的 download_detour。
const sbDirectHTTPClientTag = "ruleset-direct"

func genSingbox(target string, ver sbVersion, nodes []*link.Node, sets []normSet, customRules []string, warns *[]string, opts genOpts) *generator {
	router := target != "client"
	caps := capsFor(ver)
	mir, bypass, lan := opts.Mirror, opts.Bypass, opts.LAN

	// DNS 分流需要 geosite-cn（国内域名走本地 DNS）：用户未勾选时自动补充，
	// 否则 DNS 规则会引用一个不存在的 rule_set 导致 sing-box 启动失败
	if !hasSet(sets, "geosite-cn") {
		sets = append(sets, normSet{Tag: "geosite-cn", URL: singboxSetURL("geosite", "cn"),
			Behavior: "domain", Policy: "DIRECT", Format: "binary", Interval: 86400})
		*warns = append(*warns, "已自动补充 geosite-cn 规则集用于 DNS 分流（国内域名走本地 DNS）")
	}

	outbounds := []any{}
	nodeTags := []string{}
	for _, n := range nodes {
		if o, err := link.ToSingboxOutbound(n); err == nil {
			outbounds = append(outbounds, o)
			nodeTags = append(nodeTags, o["tag"].(string))
		}
	}
	// 注意：这里必须是 urltest（连写），不是 mihomo 的 url-test。
	// 两边同名不同写法，混用会让内核在启动时直接报 unknown outbound type。
	outbounds = append(outbounds,
		map[string]any{"type": "urltest", "tag": "Auto", "outbounds": toAnySlice(nodeTags),
			"url": "http://www.gstatic.com/generate_204", "interval": "5m"},
		map[string]any{"type": "selector", "tag": "PROXY",
			"outbounds": append([]any{"Auto"}, toAnySlice(nodeTags)...), "default": "Auto"},
		map[string]any{"type": "direct", "tag": "direct"},
	)

	// 路由规则：DNS 劫持 → 节点服务器直连 → 自定义 → 规则集 → 私网
	// hijack-dns 只对 DNS 报文生效，紧跟其后的直连规则对其它流量仍是最高优先级。
	// 少了它，TUN 模式下连节点服务器本身的那条连接会被 final:PROXY 吸进隧道，
	// 变成「用隧道连隧道端点」的回环。
	routeRules := []any{
		map[string]any{"protocol": "dns", "action": "hijack-dns"},
		// 嗅探必须排在所有域名规则之前：sniffer 只对「目标不是域名」的连接生效
		// （纯 IP 的 TLS / QUIC，典型来源是 App 内置的 HTTPDNS），不嗅这类连接
		// 就没有任何域名信息，域名规则全部落空，只剩 IP 归属可判。
		// mihomo 侧同一件事由 sniffer 段承担；这里用 1.11+ 才有的 route action
		// （minSBVersion 就是 1.11，1.10 已被拒在生成之前）。
		map[string]any{"action": "sniff"},
	}
	if len(bypass.Domains) > 0 {
		routeRules = append(routeRules,
			map[string]any{"domain": toAnySlice(bypass.Domains), "outbound": "direct"})
	}
	if len(bypass.CIDRs) > 0 {
		routeRules = append(routeRules,
			map[string]any{"ip_cidr": toAnySlice(bypass.CIDRs), "outbound": "direct"})
	}
	outboundOf := func(p string) string {
		switch p {
		case "DIRECT":
			return "direct"
		case "REJECT":
			return ""
		}
		return "PROXY"
	}
	setOut := func(m map[string]any, pol string) map[string]any {
		if pol == "REJECT" {
			m["action"] = "reject"
		} else {
			m["outbound"] = outboundOf(pol)
		}
		return m
	}
	for _, line := range customRules {
		c, ok := parseCustomRuleFull(line)
		if !ok {
			msg := "无法解析的自定义规则已跳过: " + line
			if c.Reason != "" {
				msg += "（" + c.Reason + "）"
			}
			*warns = append(*warns, msg)
			continue
		}
		*warns = append(*warns, describeCustomNotes(line, c.Notes)...)
		if c.Typ == "GEOSITE" || c.Typ == "GEOIP" {
			geoProviderFor("singbox", c.Typ, c.Val, c.Pol, &sets, mir)
			// 引用的 tag 必须与 geoProviderFor 建出来的一致（geoip-* / geosite-*）。
			// 此前这里对 GEOIP 也拼成 geosite-<val>：规则集里恰好有同名 geosite
			// 时静默用错集合，没有时 sing-box 启动 FATAL。
			routeRules = append(routeRules, setOut(map[string]any{"rule_set": []any{strings.ToLower(c.Typ) + "-" + c.Val}}, c.Pol))
			continue
		}
		// no-resolve 在 sing-box 里没有对应开关：它的 ip_cidr 规则直接比对目标
		// 地址，不做 DNS 解析。值与掩码已在解析阶段规范化，两套产物因而一致。
		body := sbRuleBody(c.Typ, c.Val)
		if body == nil {
			*warns = append(*warns, "自定义规则类型 "+c.Typ+" 无法在 sing-box 表达，已跳过: "+line)
			continue
		}
		routeRules = append(routeRules, setOut(body, c.Pol))
	}
	// 内置清单：与 mihomo 同一份数据、同一位置（自定义之后、规则集之前）。
	// 走同一条转换路径，新增规则类型时两边不会漂移。
	applyBuiltin := func(lines []string) {
		for _, line := range lines {
			typ, val, pol, ok := parseCustomRule(line)
			if !ok {
				continue // 清单是我们自己写的；解析失败只能是代码 bug，由单测兜住
			}
			body := sbRuleBody(typ, val)
			if body == nil {
				*warns = append(*warns, "内置规则类型 "+typ+" 无法在 sing-box 表达: "+line)
				continue
			}
			routeRules = append(routeRules, setOut(body, pol))
		}
	}
	// 广告拦截豁免：与 mihomo 同一份清单、同一位置（规则集之前）。sing-box 的
	// 规则是顺序匹配，放进 rule_set 之后等于没写——REJECT 先命中就 RST 了。
	sbExempt := blockExemptDecision(sets)
	if sbExempt.Insert {
		applyBuiltin(blockExemptRules)
	}
	if sbExempt.Warn != "" {
		*warns = append(*warns, sbExempt.Warn)
	}
	applyBuiltin(systemDirectRules)
	applyBuiltin(systemProxyDomains)
	// NTP 等按端口识别的流量：没有域名、没有 SNI，只能按端口兜底。
	for _, p := range systemDirectPorts {
		routeRules = append(routeRules, map[string]any{"port": []any{p}, "outbound": "direct"})
	}
	for _, s := range sets {
		routeRules = append(routeRules, setOut(map[string]any{"rule_set": []any{s.Tag}}, s.Policy))
	}
	// 裸 TCP 兜底（git over SSH 这类无 SNI 的连接），放规则集之后。
	// sing-box 没有 no-resolve 开关，但域名连接已在上面按域名命中，不会解析。
	if len(systemProxyCIDRs) > 0 {
		routeRules = append(routeRules, setOut(map[string]any{"ip_cidr": toAnySlice(systemProxyCIDRs)}, "PROXY"))
	}
	routeRules = append(routeRules, map[string]any{"ip_cidr": toAnySlice(privateCIDRs), "outbound": "direct"})

	ruleSets := []any{}
	for _, s := range sets {
		rs := map[string]any{
			"type": "remote", "tag": s.Tag, "format": s.Format,
			"url": s.URL, "update_interval": fmt.Sprintf("%ds", s.Interval),
		}
		// 规则集必须直连下载：走节点会在首启（节点还没就绪）先失败一轮。
		// 表达这件事的字段在 1.14 换了：download_detour 废弃、1.16 移除，
		// 之后写了是 unknown field 直接 FATAL；而 1.13 及以前不认识
		// http_clients。所以按版本二选一，不能只写新的。
		if caps.HTTPClient {
			rs["http_client"] = sbDirectHTTPClientTag
		} else {
			rs["download_detour"] = "direct"
		}
		ruleSets = append(ruleSets, rs)
	}

	// DNS：节点域名必须走本地解析。否则它落到 final:remote（经代理的 DoH），
	// 而代理本身还没连上——先有鸡还是先有蛋，1.11 没有 default_domain_resolver
	// 时尤其明显。国内域名走本地，其余走代理侧 DoH。
	lanTag := "local"
	if !lan.empty() {
		lanTag = "lan"
	}
	dnsRules := []any{}
	if len(bypass.Domains) > 0 {
		dnsRules = append(dnsRules,
			map[string]any{"domain": toAnySlice(bypass.Domains), "server": "local"})
	}
	// 系统关键服务用本地 DNS 解析：它们是直连流量，若落到 final:remote（经代理的
	// DoH）会拿到与直连出口不匹配的 IP，且 DoH 本身依赖代理先连通——形成死锁。
	// 与 mihomo 的 fake-ip-filter 同源派生（systemDirectDNSForms），一份清单两种写法。
	sysDomains, sysSuffixes := systemDirectDNSForms()
	if len(sysDomains) > 0 {
		dnsRules = append(dnsRules, map[string]any{"domain": toAnySlice(sysDomains), "server": "local"})
	}
	if len(sysSuffixes) > 0 {
		dnsRules = append(dnsRules, map[string]any{"domain_suffix": toAnySlice(sysSuffixes), "server": "local"})
	}
	// 局域网专用后缀（.lan / .local / .home.arpa / .localdomain）交给本地解析。
	// 与 mihomo 的 fake-ip-filter 是同一件事的另一种写法（见 localOnlySuffixes）；
	// 之前只写了 mihomo 那一侧，sing-box 用户的 NAS / 打印机主机名落在
	// final:remote，经代理问 DoH 必然失败。
	//
	// 用裸后缀是安全的：实测 sing-box 1.15 的 domain_suffix 按标签边界匹配，
	// `lan` 命中 `nas.lan` 与 `lan`，不命中 `japan`。
	//
	// 填了「局域网 DNS」时改指 lan 服务器：local 那个 tag 是 223.5.5.5（公网），
	// 名字问它一样是 NXDOMAIN，只是失败得没那么显眼。
	dnsRules = append(dnsRules, map[string]any{
		"domain_suffix": toAnySlice(localOnlySuffixes), "server": lanTag})
	dnsRules = append(dnsRules, map[string]any{"rule_set": []any{"geosite-cn"}, "server": "local"})
	if hasSet(sets, "geosite-private") {
		// 私有域规则集里就是各类内网后缀（.internal、.corp 之类），同样只有
		// 局域网解析器答得出来。mihomo 侧做不到这一条（那边只有域名策略，
		// 引 rule-set 要看内核版本行为，不赌），所以这里是 sing-box 多覆盖的一点。
		dnsRules = append(dnsRules, map[string]any{"rule_set": []any{"geosite-private"}, "server": lanTag})
	}
	dns := map[string]any{
		"servers": sbDNSServers(caps, lan),
		"rules":   dnsRules,
		"final":   "remote",
	}
	if notice := lanDNSNotice(lan, router); notice != "" {
		*warns = append(*warns, notice)
	}

	inbounds := []any{}
	if router {
		tun := map[string]any{
			"type": "tun", "tag": "tun-in",
			"address": []any{"172.19.0.1/30"},
			// 与 mihomo 同理：sing-box 默认 mtu 9000，不显式压到 1500 会在
			// 真实链路（1500）上丢大包，表现为大传输卡死、小请求正常。
			// 链路是 PPPoE（1492）时可降到 1420。
			"mtu":          1500,
			"auto_route":   true,
			"strict_route": true,
		}
		// 1.15 起 sing-tun 用自有协议栈，stack 字段废弃（1.17 移除）。
		// 写了不会报错，但会在日志里刷 deprecated 警告，干净起见直接不写。
		if caps.TunStack {
			tun["stack"] = "mixed"
		}
		inbounds = append(inbounds, tun)
	}
	inbounds = append(inbounds, map[string]any{
		"type": "mixed", "tag": "mixed-in",
		"listen":      map[bool]string{true: "0.0.0.0", false: "127.0.0.1"}[router],
		"listen_port": 7890,
	})

	route := map[string]any{
		"rules":                 routeRules,
		"rule_set":              ruleSets,
		"final":                 "PROXY",
		"auto_detect_interface": true,
	}
	// 1.12 才有 default_domain_resolver。1.11 写这个字段是 unknown field，
	// 直接 FATAL——这是实测踩到的，不是理论差异。
	if caps.DomainResolver {
		route["default_domain_resolver"] = map[string]any{"server": "local"}
	}

	doc := map[string]any{
		"log":       map[string]any{"level": map[bool]string{true: "info", false: "warn"}[router], "timestamp": true},
		"dns":       dns,
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"route":     route,
	}
	// 1.14+ 的规则集下载走这个客户端。**不写 detour**：写了 `detour: "direct"`
	// 会直接 FATAL（`detour to an empty direct outbound makes no sense`——
	// 内置 direct 出站没有字段，detour 到它等于没 detour）。不写 detour
	// 的客户端是直连，正是我们要的：走路由的话 final 是 PROXY，节点没就绪时
	// 首启就会下载失败。
	if caps.HTTPClient {
		doc["http_clients"] = []any{map[string]any{"tag": sbDirectHTTPClientTag}}
	}

	// JSON 无注释；说明放 UI 提示与 README
	return &generator{doc: doc}
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func hasSet(sets []normSet, tag string) bool {
	for _, s := range sets {
		if s.Tag == tag {
			return true
		}
	}
	return false
}
