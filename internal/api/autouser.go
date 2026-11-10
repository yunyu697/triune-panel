package api

// 新增入站时自动建的那个用户。
//
// 为什么要有它：入站建好、开关打开、应用成功，界面上一片绿，但这个入站
// 底下一个人都没有——导不出链接、订阅是空的、客户端连不上。用户看着「一切
// 正常」的页面去找问题，实际上面板从来没告诉过他「还得去用户页建一个人」。
//
// 三条约束：
//  1. 只在请求里**没有** clients 字段时补一个。从 JSON 导入带了用户的，
//     一律按导入的信息建（用户确认过的凭据不能被随机值顶掉）；导入时取消
//     勾选「一并创建用户」的，前端会显式传 `clients: []`，这里就不补。
//  2. 名字是 8 位随机串。导入的配置里 u1 / user1 满地都是，自动生成的再叫
//     user1 必然和真实用户撞名；而三个内核里同名用户等于同一个人
//     （xray 的 email、sing-box 的 name 都要求唯一）。
//  3. 协议本身没有「用户」概念的不建：direct、wireguard，以及没开密码认证的
//     socks / http / mixed。建了没人用得上，只会在用户页里多一条看不懂的记录。

import (
	"crypto/rand"
	"encoding/json"
	"strings"

	"triune/internal/model"
)

// autoUserNameAlphabet 去掉了 l / 1 / o / 0：这串名字是要被人抄进客户端的。
// 长度 32，而单个字节有 256 种取值，取模不会引入偏置。
const autoUserNameAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// autoUserNameLen 用户名长度：8 位，32^8 ≈ 1.1e12 种组合。
const autoUserNameLen = 8

// randomUserName 生成一个 8 位随机用户名。
func randomUserName() string {
	b := make([]byte, autoUserNameLen)
	if _, err := rand.Read(b); err != nil {
		// 熵源出问题也不该让「建入站」失败：退回十六进制随机串，长度仍是 8
		return randSecret(4)
	}
	out := make([]byte, autoUserNameLen)
	for i, v := range b {
		out[i] = autoUserNameAlphabet[int(v)%len(autoUserNameAlphabet)]
	}
	return string(out)
}

// uniqueAutoUserName 生成一个不与现有用户重名的名字。
//
// 8 位随机串之间撞名的概率可以忽略，这里查一遍是防另一件事：老数据里可能
// 真有一个叫 `k3m9x2qa` 的用户（人自己起的），撞上去内核会把它当同一个人。
func (a *Server) uniqueAutoUserName() string {
	used := map[string]bool{}
	if cs, err := a.Store.ListClients(); err == nil {
		for _, c := range cs {
			used[c.Name] = true
		}
	}
	for i := 0; i < 8; i++ {
		if n := randomUserName(); !used[n] {
			return n
		}
	}
	// 连续 8 次都撞（实际不可能）：加长到 10 位，仍然守住「短、好抄」
	return randomUserName() + randomUserName()[:2]
}

// autoInboundClient 决定这条入站要不要顺手建一个用户，要建就返回规格。
//
// 凭据沿用与 JSON 导入完全相同的一套规则（generateCredential）：vless / vmess /
// tuic 给 UUID，trojan / shadowsocks / hysteria2 / anytls 给密码，socks / http /
// mixed 只在开了密码认证时给 `用户名:密码`。返回空串就是这个协议没有用户概念，
// 不建。
func (a *Server) autoInboundClient(in *model.Inbound) (inboundClientSpec, bool) {
	cred, _ := generateCredential(in.Protocol, settingsMapOf(in.Settings))
	if cred == "" {
		return inboundClientSpec{}, false
	}
	return inboundClientSpec{Name: a.uniqueAutoUserName(), Credential: cred}, true
}

// settingsMapOf 把入站的 settings JSON 解成 map；解不开就当没有（交给后续渲染报错）。
func settingsMapOf(s string) map[string]any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}
