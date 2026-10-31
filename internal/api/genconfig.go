package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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
}

type setPolicy struct {
	ID     string `json:"id"`
	Policy string `json:"policy"` // DIRECT | PROXY | REJECT
}

type scenarioDef struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Tag   string      `json:"tag"`   // 场景分类标签：通用 / 流媒体 / AI / 游戏 ...
	Desc  string      `json:"desc"`
	Sets  []setPolicy `json:"sets"`
	Rules []string    `json:"rules"` // 场景内置的自定义规则（clash 语法）
}

// ruleCatalog 规则集目录（geosite 类目名与 v2fly domain-list-community / MetaCubeX / SagerNet 对齐）
// ruleCatalog 规则集目录
// 所有条目均已对上游真实文件校验：
//   mihomo   → MetaCubeX/meta-rules-dat  meta/geo/{geosite|geoip}/{name}.mrs
//   sing-box → SagerNet/sing-geosite      rule-set/geosite-{name}.srs
//              SagerNet/sing-geoip       rule-set/geoip-{name}.srs
// Cores 标记该规则集在哪些内核的上游存在（geoip-private / category-porn 仅 mihomo 提供）。
var ruleCatalog = []ruleSetDef{
	{ID: "geosite-cn", Name: "中国大陆域名", Dir: "geosite", Behavior: "domain", Group: "基础分流", Cores: "both"},
	{ID: "geoip-cn", Name: "中国大陆 IP", Dir: "geoip", Behavior: "ipcidr", Group: "基础分流", Cores: "both"},
	{ID: "geosite-private", Name: "局域网 / 私有域名", Dir: "geosite", Behavior: "domain", Group: "基础分流", Cores: "both"},
	{ID: "geoip-private", Name: "局域网 / 私有 IP", Dir: "geoip", Behavior: "ipcidr", Group: "基础分流", Cores: "mihomo"},
	{ID: "geosite-geolocation-!cn", Name: "非中国大陆域名", Dir: "geosite", Behavior: "domain", Group: "基础分流", Cores: "both"},
	{ID: "geosite-category-ads-all", Name: "广告域名（通用全量）", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both"},
	{ID: "geosite-category-ads", Name: "广告域名（精简）", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both"},
	{ID: "geosite-category-public-tracker", Name: "公共追踪器", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both"},
	{ID: "geosite-category-telemetry", Name: "遥测 / 数据上报", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both"},
	{ID: "geosite-category-consent-management", Name: "Cookie 同意管理", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both"},
	{ID: "geosite-category-password-management", Name: "密码管理器", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both"},
	{ID: "geosite-category-ip-geo-detect", Name: "IP 归属地检测", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both"},
	{ID: "geosite-category-anticensorship", Name: "反审查站点", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both"},
	{ID: "geosite-category-proxy-tunnels", Name: "代理隧道与中转", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both"},
	{ID: "geosite-category-vpnservices", Name: "VPN 服务商", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both"},
	{ID: "geosite-category-stun", Name: "STUN / TURN 服务", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both"},
	{ID: "geosite-category-ipfs", Name: "IPFS 节点", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "both"},
	{ID: "geosite-openai", Name: "OpenAI / ChatGPT", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both"},
	{ID: "geosite-anthropic", Name: "Anthropic / Claude", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both"},
	{ID: "geosite-category-ai-!cn", Name: "AI 服务（海外）", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both"},
	{ID: "geosite-category-ai-chat-!cn", Name: "AI 对话（海外）", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both"},
	{ID: "geosite-category-ai-cn", Name: "AI 服务（国内）", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both"},
	{ID: "geosite-category-ai-ru", Name: "AI 服务（俄罗斯区）", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both"},
	{ID: "geosite-google-gemini", Name: "Google Gemini", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both"},
	{ID: "geosite-perplexity", Name: "Perplexity", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both"},
	{ID: "geosite-deepseek", Name: "DeepSeek", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both"},
	{ID: "geosite-doubao", Name: "豆包 Doubao", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both"},
	{ID: "geosite-bing", Name: "Bing / Copilot", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both"},
	{ID: "geosite-huggingface", Name: "Hugging Face", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both"},
	{ID: "geosite-notion", Name: "Notion", Dir: "geosite", Behavior: "domain", Group: "AI 服务", Cores: "both"},
	{ID: "geosite-netflix", Name: "Netflix", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-disney", Name: "Disney+", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-youtube", Name: "YouTube", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-tiktok", Name: "TikTok", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-spotify", Name: "Spotify", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-hbo", Name: "HBO", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-bahamut", Name: "巴哈姆特动漫", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-twitch", Name: "Twitch", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-netease", Name: "网易云音乐", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-bilibili", Name: "哔哩哔哩", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-iqiyi", Name: "爱奇艺", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-youku", Name: "优酷", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-category-media", Name: "影音媒体站点", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-category-media-cn", Name: "国内影音站点", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-category-entertainment", Name: "娱乐站点", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-category-entertainment-cn", Name: "国内娱乐站点", Dir: "geosite", Behavior: "domain", Group: "流媒体影音", Cores: "both"},
	{ID: "geosite-telegram", Name: "Telegram", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both"},
	{ID: "geosite-twitter", Name: "X / Twitter", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both"},
	{ID: "geosite-facebook", Name: "Facebook", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both"},
	{ID: "geosite-instagram", Name: "Instagram", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both"},
	{ID: "geosite-whatsapp", Name: "WhatsApp", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both"},
	{ID: "geosite-line", Name: "LINE", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both"},
	{ID: "geosite-discord", Name: "Discord", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both"},
	{ID: "geosite-reddit", Name: "Reddit", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both"},
	{ID: "geosite-viber", Name: "Viber", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both"},
	{ID: "geosite-signal", Name: "Signal", Dir: "geosite", Behavior: "domain", Group: "社交通讯", Cores: "both"},
	{ID: "geosite-category-games", Name: "游戏平台（综合）", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both"},
	{ID: "geosite-category-games-cn", Name: "国内游戏平台", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both"},
	{ID: "geosite-category-game-accelerator-cn", Name: "国内游戏加速服务", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both"},
	{ID: "geosite-category-enhance-gaming", Name: "游戏加速增强", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both"},
	{ID: "geosite-category-flight-simulator", Name: "模拟器类游戏", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both"},
	{ID: "geosite-steam", Name: "Steam", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both"},
	{ID: "geosite-epicgames", Name: "Epic Games", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both"},
	{ID: "geosite-playstation", Name: "PlayStation", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both"},
	{ID: "geosite-xbox", Name: "Xbox", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both"},
	{ID: "geosite-nintendo", Name: "Nintendo", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both"},
	{ID: "geosite-blizzard", Name: "暴雪战网", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both"},
	{ID: "geosite-mihoyo", Name: "米哈游（HoYoPlay）", Dir: "geosite", Behavior: "domain", Group: "游戏加速", Cores: "both"},
	{ID: "geosite-github", Name: "GitHub", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both"},
	{ID: "geosite-gitlab", Name: "GitLab", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both"},
	{ID: "geosite-docker", Name: "Docker Hub", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both"},
	{ID: "geosite-golang", Name: "Go 语言站点", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both"},
	{ID: "geosite-nodejs", Name: "Node.js / npm", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both"},
	{ID: "geosite-jetbrains", Name: "JetBrains", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both"},
	{ID: "geosite-category-dev", Name: "开发者站点", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both"},
	{ID: "geosite-category-dev-cn", Name: "国内开发者站点", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both"},
	{ID: "geosite-category-cdn-!cn", Name: "海外 CDN", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both"},
	{ID: "geosite-category-cdn-cn", Name: "国内 CDN", Dir: "geosite", Behavior: "domain", Group: "开发与技术", Cores: "both"},
	{ID: "geosite-category-finance", Name: "金融理财站点", Dir: "geosite", Behavior: "domain", Group: "金融商务", Cores: "both"},
	{ID: "geosite-category-bank-cn", Name: "国内银行", Dir: "geosite", Behavior: "domain", Group: "金融商务", Cores: "both"},
	{ID: "geosite-category-securities-cn", Name: "国内证券", Dir: "geosite", Behavior: "domain", Group: "金融商务", Cores: "both"},
	{ID: "geosite-category-cryptocurrency", Name: "加密货币", Dir: "geosite", Behavior: "domain", Group: "金融商务", Cores: "both"},
	{ID: "geosite-paypal", Name: "PayPal", Dir: "geosite", Behavior: "domain", Group: "金融商务", Cores: "both"},
	{ID: "geosite-stripe", Name: "Stripe", Dir: "geosite", Behavior: "domain", Group: "金融商务", Cores: "both"},
	{ID: "geosite-wise", Name: "Wise 转账", Dir: "geosite", Behavior: "domain", Group: "金融商务", Cores: "both"},
	{ID: "geosite-amazon", Name: "Amazon", Dir: "geosite", Behavior: "domain", Group: "购物电商", Cores: "both"},
	{ID: "geosite-ebay", Name: "eBay", Dir: "geosite", Behavior: "domain", Group: "购物电商", Cores: "both"},
	{ID: "geosite-pinduoduo", Name: "拼多多", Dir: "geosite", Behavior: "domain", Group: "购物电商", Cores: "both"},
	{ID: "geosite-category-ecommerce", Name: "电商平台", Dir: "geosite", Behavior: "domain", Group: "购物电商", Cores: "both"},
	{ID: "geosite-microsoft", Name: "Microsoft", Dir: "geosite", Behavior: "domain", Group: "办公云服务", Cores: "both"},
	{ID: "geosite-google", Name: "Google", Dir: "geosite", Behavior: "domain", Group: "办公云服务", Cores: "both"},
	{ID: "geosite-dropbox", Name: "Dropbox", Dir: "geosite", Behavior: "domain", Group: "办公云服务", Cores: "both"},
	{ID: "geosite-zoom", Name: "Zoom 会议", Dir: "geosite", Behavior: "domain", Group: "办公云服务", Cores: "both"},
	{ID: "geosite-onedrive", Name: "OneDrive", Dir: "geosite", Behavior: "domain", Group: "办公云服务", Cores: "both"},
	{ID: "geosite-icloud", Name: "iCloud", Dir: "geosite", Behavior: "domain", Group: "办公云服务", Cores: "both"},
	{ID: "geosite-category-companies", Name: "大型企业站点", Dir: "geosite", Behavior: "domain", Group: "办公云服务", Cores: "both"},
	{ID: "geosite-category-scholar-cn", Name: "国内学术资源", Dir: "geosite", Behavior: "domain", Group: "教育科研", Cores: "both"},
	{ID: "geosite-category-scholar-!cn", Name: "海外学术资源", Dir: "geosite", Behavior: "domain", Group: "教育科研", Cores: "both"},
	{ID: "geosite-category-scholar-hk", Name: "港澳学术资源", Dir: "geosite", Behavior: "domain", Group: "教育科研", Cores: "both"},
	{ID: "geosite-category-mooc-cn", Name: "国内慕课平台", Dir: "geosite", Behavior: "domain", Group: "教育科研", Cores: "both"},
	{ID: "geosite-category-education-cn", Name: "国内教育站点", Dir: "geosite", Behavior: "domain", Group: "教育科研", Cores: "both"},
	{ID: "geosite-category-documents-cn", Name: "国内文档站", Dir: "geosite", Behavior: "domain", Group: "教育科研", Cores: "both"},
	{ID: "geosite-category-hospital-cn", Name: "国内医院与医疗", Dir: "geosite", Behavior: "domain", Group: "生活服务", Cores: "both"},
	{ID: "geosite-category-food-cn", Name: "国内餐饮外卖", Dir: "geosite", Behavior: "domain", Group: "生活服务", Cores: "both"},
	{ID: "geosite-category-logistics-cn", Name: "国内物流", Dir: "geosite", Behavior: "domain", Group: "生活服务", Cores: "both"},
	{ID: "geosite-category-blog-cn", Name: "国内博客", Dir: "geosite", Behavior: "domain", Group: "生活服务", Cores: "both"},
	{ID: "geosite-category-porn", Name: "成人内容（拦截用）", Dir: "geosite", Behavior: "domain", Group: "广告与隐私", Cores: "mihomo"},
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
			{ID: "geosite-deepseek", Policy: "PROXY"},
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
	Core        string         `json:"core"` // mihomo | singbox
	Target      string         `json:"target"`
	InboundIDs  []int64        `json:"inbound_ids"`
	Scenario    string         `json:"scenario"`
	Sets        []setPolicy    `json:"sets"`
	CustomRules []string       `json:"custom_rules"`
	CustomSets  []customSetReq `json:"custom_sets"`
}

// handleExportCatalog GET /api/export/catalog：规则集目录 + 场景预设
func (a *Server) handleExportCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"sets":      ruleCatalog,
		"scenarios": scenarioPresets,
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
	if q.Target != "client" && q.Target != "main-router" && q.Target != "side-router" {
		fail(w, 400, "target 必须是 client / main-router / side-router")
		return
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

	// 解析规则集选择 + 自定义规则集
	sets, warns := normalizeSets(q)
	var g *generator
	if q.Core == "mihomo" {
		g = genMihomo(q.Target, nodes, sets, q.CustomRules, &warns)
	} else {
		g = genSingbox(q.Target, nodes, sets, q.CustomRules, &warns)
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
	writeJSON(w, 200, map[string]any{
		"filename":     fmt.Sprintf("%s-%s.%s", safeName, q.Core, map[string]string{"mihomo": "yaml", "singbox": "json"}[q.Core]),
		"content":      content,
		"content_type": ct,
		"warnings":     warns,
	})
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
	seen := map[string]bool{}
	var out []normSet
	add := func(s normSet) {
		if seen[s.Tag] {
			return
		}
		seen[s.Tag] = true
		out = append(out, s)
	}
	policy := func(p string) string {
		switch p {
		case "DIRECT", "PROXY", "REJECT":
			return p
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
		if q.Core == "mihomo" {
			add(normSet{Tag: def.ID, URL: mihomoSetURL(def.Dir, strings.TrimPrefix(strings.TrimPrefix(def.ID, "geosite-"), "geoip-")),
				Behavior: def.Behavior, Policy: policy(s.Policy), Format: "mrs", Interval: 86400})
		} else {
			add(normSet{Tag: def.ID, URL: singboxSetURL(def.Dir, strings.TrimPrefix(strings.TrimPrefix(def.ID, "geosite-"), "geoip-")),
				Behavior: def.Behavior, Policy: policy(s.Policy), Format: "binary", Interval: 86400})
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
		add(normSet{Tag: tag, URL: cs.URL, Behavior: beh, Policy: policy(cs.Policy), Format: fmt, Interval: interval})
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

// parseCustomRule 解析 clash 风格自定义规则：TYPE,VALUE,POLICY[,no-resolve]
// 支持 DOMAIN / DOMAIN-SUFFIX / DOMAIN-KEYWORD / IP-CIDR / IP-CIDR6 / GEOSITE / GEOIP
func parseCustomRule(line string) (typ, val, pol string, ok bool) {
	parts := strings.Split(strings.TrimSpace(line), ",")
	if len(parts) < 3 {
		return "", "", "", false
	}
	typ = strings.ToUpper(strings.TrimSpace(parts[0]))
	pol = strings.ToUpper(strings.TrimSpace(parts[len(parts)-1]))
	switch pol {
	case "DIRECT", "PROXY", "REJECT":
	default:
		return "", "", "", false
	}
	body := parts[1 : len(parts)-1]
	val = strings.TrimSpace(strings.Join(body, ","))
	switch typ {
	case "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD", "IP-CIDR", "IP-CIDR6", "GEOSITE", "GEOIP":
		if val == "" {
			return "", "", "", false
		}
		return typ, val, pol, true
	}
	return "", "", "", false
}

// geoProviderFor GEOSITE/GEOIP 自定义规则需要的动态 provider
func geoProviderFor(core, typ, val, pol string, sets *[]normSet) {
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
		s = normSet{Tag: tag, URL: mihomoSetURL(dir, val), Behavior: beh, Policy: pol, Format: "mrs", Interval: 86400}
	} else {
		s = normSet{Tag: tag, URL: singboxSetURL(dir, val), Behavior: beh, Policy: pol, Format: "binary", Interval: 86400}
	}
	*sets = append(*sets, s)
}

var privateCIDRs = []string{
	"192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12",
	"127.0.0.0/8", "169.254.0.0/16", "::1/128", "fc00::/7", "fe80::/10",
}

var fakeIPFilter = []string{
	"*.lan", "*.local", "+.msftconnecttest.com", "+.msftncsi.com",
	"+.stun.*", "+.srv.nintendo.net", "+.stun.playstation.net",
	"xbox.*.microsoft.com", "+.xboxlive.com", "time.*.com", "ntp.*.com",
}

// targetNotes 各目标的使用说明（YAML 头注释 / UI 提示）
func targetNote(target string) string {
	switch target {
	case "main-router":
		return "主路由模式：TUN 接管全机流量，DNS 劫持 :53，局域网设备无需任何配置。\n重启路由前建议先在单设备上验证配置。"
	case "side-router":
		return "旁路由模式：将本机 IP 设为局域网设备的网关与 DNS 即可接管；\n主路由无需改动（关闭主路由 DHCP 冲突即可并存）。"
	default:
		return "客户端模式：默认使用混合端口 7890（HTTP/SOCKS），配合系统代理或浏览器插件使用。"
	}
}

// ==================== mihomo 生成器 ====================

func genMihomo(target string, nodes []*link.Node, sets []normSet, customRules []string, warns *[]string) *generator {
	router := target != "client"

	proxies := []any{}
	names := []string{}
	for _, n := range nodes {
		if p, err := link.ToClash(n); err == nil {
			proxies = append(proxies, p)
			names = append(names, p["name"].(string))
		}
	}

	// 规则：自定义 → 规则集 → 私网 → MATCH（normalizeSets 已按 REJECT>PROXY>DIRECT 排序）
	rules := []any{}
	for _, line := range customRules {
		typ, val, pol, ok := parseCustomRule(line)
		if !ok {
			*warns = append(*warns, "无法解析的自定义规则已跳过: "+line)
			continue
		}
		if typ == "GEOSITE" || typ == "GEOIP" {
			geoProviderFor("mihomo", typ, val, pol, &sets)
			rules = append(rules, fmt.Sprintf("RULE-SET,geosite-%s,%s", val, pol))
			if typ == "GEOIP" {
				rules[len(rules)-1] = fmt.Sprintf("RULE-SET,geoip-%s,%s", val, pol)
			}
			continue
		}
		rules = append(rules, fmt.Sprintf("%s,%s,%s", typ, val, pol))
	}
	for _, s := range sets {
		rules = append(rules, fmt.Sprintf("RULE-SET,%s,%s", s.Tag, s.Policy))
	}
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
			"enable": true, "stack": "mixed",
			"auto-route": true, "auto-redirect": true,
			"auto-detect-interface": true,
			"dns-hijack":            []any{"any:53"},
		}
	}

	dns := map[string]any{
		"enable":             true,
		"ipv6":               false,
		"enhanced-mode":      map[bool]string{true: "fake-ip", false: "redir-host"}[router],
		"fake-ip-range":      "198.18.0.1/16",
		"fake-ip-filter":     toAnySlice(fakeIPFilter),
		"nameserver":         toAnySlice([]string{"https://223.5.5.5/dns-query", "https://doh.pub/dns-query"}),
		"default-nameserver": toAnySlice([]string{"223.5.5.5", "119.29.29.29"}),
	}
	if router {
		dns["listen"] = "0.0.0.0:53"
	}

	doc := map[string]any{
		"mode":                "rule",
		"log-level":           map[bool]string{true: "info", false: "warning"}[router],
		"allow-lan":           router,
		"bind-address":        "*",
		"mixed-port":          7890,
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
		doc["sniffer"] = map[string]any{
			"enable": true, "parse-pure-ip": true, "override-destination": false,
			"sniff": map[string]any{"HTTP": map[string]any{"ports": []any{80, "8080-8880"}}, "TLS": map[string]any{"ports": []any{443, 8443}}},
		}
	}

	note := strings.ReplaceAll(targetNote(target), "\n", "\n# ")
	header := "# Triune Panel 生成 | mihomo | " + target + "\n# " + note + "\n\n"
	return &generator{doc: doc, header: header}
}

// ==================== sing-box 生成器 ====================

func genSingbox(target string, nodes []*link.Node, sets []normSet, customRules []string, warns *[]string) *generator {
	router := target != "client"

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
	outbounds = append(outbounds,
		map[string]any{"type": "urltest", "tag": "Auto", "outbounds": toAnySlice(nodeTags),
			"url": "http://www.gstatic.com/generate_204", "interval": "5m"},
		map[string]any{"type": "selector", "tag": "PROXY",
			"outbounds": append([]any{"Auto"}, toAnySlice(nodeTags)...), "default": "Auto"},
		map[string]any{"type": "direct", "tag": "direct"},
	)

	// 路由规则：DNS 劫持 → 自定义 → 规则集 → 私网
	routeRules := []any{
		map[string]any{"protocol": "dns", "action": "hijack-dns"},
	}
	// 返回规则的出站字段：REJECT 用 action（block 出站已在 1.13 移除）
	ruleReject := func() map[string]any { return map[string]any{"action": "reject"} }
	_ = ruleReject
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
		typ, val, pol, ok := parseCustomRule(line)
		if !ok {
			*warns = append(*warns, "无法解析的自定义规则已跳过: "+line)
			continue
		}
		switch typ {
		case "DOMAIN":
			routeRules = append(routeRules, setOut(map[string]any{"domain": []any{val}}, pol))
		case "DOMAIN-SUFFIX":
			routeRules = append(routeRules, setOut(map[string]any{"domain_suffix": []any{val}}, pol))
		case "DOMAIN-KEYWORD":
			routeRules = append(routeRules, setOut(map[string]any{"domain_keyword": []any{val}}, pol))
		case "IP-CIDR", "IP-CIDR6":
			if !strings.Contains(val, "/") {
				val += map[bool]string{true: "/128", false: "/32"}[strings.Contains(val, ":")]
			}
			routeRules = append(routeRules, setOut(map[string]any{"ip_cidr": []any{val}}, pol))
		case "GEOSITE", "GEOIP":
			geoProviderFor("singbox", typ, val, pol, &sets)
			routeRules = append(routeRules, setOut(map[string]any{"rule_set": []any{"geosite-" + val}}, pol))
		}
	}
	for _, s := range sets {
		routeRules = append(routeRules, setOut(map[string]any{"rule_set": []any{s.Tag}}, s.Policy))
	}
	routeRules = append(routeRules, map[string]any{"ip_cidr": toAnySlice(privateCIDRs), "outbound": "direct"})

	ruleSets := []any{}
	for _, s := range sets {
		ruleSets = append(ruleSets, map[string]any{
			"type": "remote", "tag": s.Tag, "format": s.Format,
			"url": s.URL, "download_detour": "direct", "update_interval": fmt.Sprintf("%ds", s.Interval),
		})
	}

	// DNS：国内域名走本地，其余走代理侧 DoH
	dnsRules := []any{map[string]any{"rule_set": []any{"geosite-cn"}, "server": "local"}}
	if hasSet(sets, "geosite-private") {
		dnsRules = append(dnsRules, map[string]any{"rule_set": []any{"geosite-private"}, "server": "local"})
	}
	dns := map[string]any{
		// sing-box 1.12+ 新 DNS server 格式（旧 address 格式已在 1.14 移除）
		"servers": []any{
			map[string]any{"type": "https", "tag": "remote", "server": "8.8.8.8", "detour": "PROXY"},
			map[string]any{"type": "udp", "tag": "local", "server": "223.5.5.5", "detour": "direct"},
		},
		"rules": dnsRules,
		"final": "remote",
	}

	inbounds := []any{}
	if router {
		inbounds = append(inbounds, map[string]any{
			"type": "tun", "tag": "tun-in",
			"address":      []any{"172.19.0.1/30"},
			"auto_route":   true,
			"strict_route": true,
			"stack":        "mixed",
		})
	}
	inbounds = append(inbounds, map[string]any{
		"type": "mixed", "tag": "mixed-in",
		"listen":      map[bool]string{true: "0.0.0.0", false: "127.0.0.1"}[router],
		"listen_port": 7890,
	})

	doc := map[string]any{
		"log":       map[string]any{"level": map[bool]string{true: "info", false: "warn"}[router], "timestamp": true},
		"dns":       dns,
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"route": map[string]any{
			"rules":                   routeRules,
			"rule_set":                ruleSets,
			"final":                   "PROXY",
			"auto_detect_interface":   true,
			"default_domain_resolver": map[string]any{"server": "local"},
		},
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
