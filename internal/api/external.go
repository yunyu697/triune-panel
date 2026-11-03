package api

// 外部节点：只进订阅聚合，不生成本地配置。
//
// 这个页面原先只有一个列表（名称/协议/地址/时间），而协议和地址是**前端用
// new URL() 猜的**：vmess:// 与 ss:// 的正文是 base64，URL 解析出来是错的东西，
// 列表上的「地址」根本不是节点地址。同时订阅渲染时 link.Parse 失败会被静默
// 跳过（见 subscription.go），于是「导入成功」的节点可能从来不出现在订阅里，
// 用户看着列表只会觉得「明明加进去了怎么没有」。
//
// 现在列表由后端给出**实时解析摘要**，解析失败明说原因；导入按「同一条链接」与
// 「同一个节点」（忽略备注名）两级去重；节点可以改名 / 换链接。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"triune/internal/link"
	"triune/internal/model"
)

// ---- external nodes ----

// extParsed 一条外部节点链接的解析结果。
// OK=false 时 Error 说明原因，其余字段留空。
type extParsed struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Network  string `json:"network,omitempty"`
	Security string `json:"security,omitempty"`
	SNI      string `json:"sni,omitempty"`
}

// extView 对外返回的形状：库里那几列（内嵌展开）+ parsed。
// 内嵌指针让顶层 JSON 字段与旧接口完全一致，老调用方不用改。
type extView struct {
	*model.ExternalNode
	Parsed extParsed `json:"parsed"`
}

func describeExternal(raw string) extParsed {
	n, err := link.Parse(raw)
	if err != nil {
		return extParsed{Error: err.Error()}
	}
	return extParsed{
		OK: true, Protocol: n.Protocol, Host: n.Host, Port: n.Port,
		Network: n.Network, Security: n.Security, SNI: n.SNI,
	}
}

func (a *Server) listExternal(w http.ResponseWriter, r *http.Request) {
	ns, err := a.Store.ListExternalNodes()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	// 每次请求现算摘要：链接是唯一真相。以后解析器多认一种协议，历史节点
	// 自动跟着变；存库反而会留一份迟早过期的快照。200 条也就微秒级。
	out := make([]extView, 0, len(ns))
	for _, n := range ns {
		out = append(out, extView{ExternalNode: n, Parsed: describeExternal(n.RawLink)})
	}
	writeJSON(w, 200, out)
}

// extKey 返回一条链接的「节点身份」：只取连接参数，**不含备注名**。
//
// 订阅源更新后常会把节点改个名。若只按 RawLink 精确去重，整份订阅会被再导入
// 一遍，用户订阅里就出现两份同一台服务器（客户端还会因重名改成 xxx 2）。
// 同一服务器 + 同一凭据 + 同一传输 = 同一个节点，名字只是标签。
//
// 返回值含凭据，只在内存里当 map key 用，不落库、不打日志。
func extKey(n *link.Node) string {
	return strings.Join([]string{
		n.Protocol, n.Host, strconv.Itoa(n.Port), n.Cred,
		n.Security, n.SNI, n.Network, n.Path,
	}, "\x1f")
}

// extIndex 去重索引：链接原文与节点身份两级都查，任一级命中就算已存在。
type extIndex struct {
	raw  map[string]bool   // RawLink → 存在
	iden map[string]string // 身份 → 已有节点的名称（用于提示「和谁重复」）
}

// newExtIndex 读现有节点建索引。skipID 非 0 时跳过该条，
// 供「改链接」自查重时排除自己——否则拿自己比，永远算重复。
func (a *Server) newExtIndex(skipID int64) (*extIndex, error) {
	existing, err := a.Store.ListExternalNodes()
	if err != nil {
		return nil, err
	}
	ix := &extIndex{raw: map[string]bool{}, iden: map[string]string{}}
	for _, e := range existing {
		if skipID != 0 && e.ID == skipID {
			continue
		}
		ix.raw[e.RawLink] = true
		if n, err := link.Parse(e.RawLink); err == nil {
			ix.iden[extKey(n)] = e.Name
		}
	}
	return ix, nil
}

// dup 判断一条节点是否已存在；返回非空字符串即为跳过原因。
func (ix *extIndex) dup(n *link.Node) string {
	if ix.raw[n.Raw] {
		return "这条链接已存在"
	}
	if name, ok := ix.iden[extKey(n)]; ok {
		return "和「" + name + "」是同一个节点（只是备注不同）"
	}
	return ""
}

func (ix *extIndex) add(n *link.Node, name string) {
	ix.raw[n.Raw] = true
	ix.iden[extKey(n)] = name
}

// mustIndex 是给「单条新增」用的薄封装：索引建不出来时退回空索引，
// 宁可少拦一次重复，也不要因为这个原因拒掉用户明确要加的节点。
func (a *Server) mustIndex() *extIndex {
	if ix, err := a.newExtIndex(0); err == nil {
		return ix
	}
	return &extIndex{raw: map[string]bool{}, iden: map[string]string{}}
}

// addExternalNodes 把已解析的节点写入外部节点表，按链接/身份两级去重。
// prefix 非空时给名字加前缀（拉远端订阅时用来区分来源）。
//
// 三个返回值都是给人看的中文描述，不是 id：调用方直接塞进响应与审计即可。
func (a *Server) addExternalNodes(nodes []*link.Node, prefix string) (created, skipped, failed []string) {
	// 三个都初始化成空切片：JSON 序列化后是 []，不是 null。
	// 前端写 (j.created||[]).length 能扛住 null，但接口不该把「空」表达成 null。
	created, skipped, failed = []string{}, []string{}, []string{}
	ix, err := a.newExtIndex(0)
	if err != nil {
		return created, skipped, []string{"读取现有节点失败: " + err.Error()}
	}
	for _, n := range nodes {
		if n.Raw == "" {
			if l := link.GenLink(n); l != "" {
				n.Raw = l
			} else {
				failed = append(failed, orDefault(n.Name, n.Host)+": 无法生成链接")
				continue
			}
		}
		if why := ix.dup(n); why != "" {
			skipped = append(skipped, orDefault(n.Name, n.Host)+"（"+why+"）")
			continue
		}
		// 先定名字再拼前缀：否则无名节点的名字会变成「HK-」后面跟着空字符串
		name := orDefault(n.Name, n.Host)
		if prefix != "" {
			name = prefix + "-" + name
		}
		en := &model.ExternalNode{Name: name, RawLink: n.Raw}
		if err := a.Store.CreateExternalNode(en); err != nil {
			failed = append(failed, name+": "+err.Error())
			continue
		}
		ix.add(n, name)
		created = append(created, name+"（"+n.Protocol+"）")
	}
	return created, skipped, failed
}

func (a *Server) addExternal(w http.ResponseWriter, r *http.Request) {
	var n model.ExternalNode
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		fail(w, 400, err.Error())
		return
	}
	raw := strings.TrimSpace(n.RawLink)
	// 解析一次：既做校验，也拿到摘要与去重用的身份
	node, err := link.Parse(raw)
	if err != nil {
		fail(w, 400, "链接无法解析: "+err.Error())
		return
	}
	if why := a.mustIndex().dup(node); why != "" {
		fail(w, 409, why)
		return
	}
	name := orDefault(strings.TrimSpace(n.Name), orDefault(node.Name, node.Host))
	en := &model.ExternalNode{Name: name, RawLink: raw}
	if err := a.Store.CreateExternalNode(en); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditExternal, en.Name, "新增外部节点")
	writeJSON(w, 200, extView{ExternalNode: en, Parsed: describeExternal(raw)})
}

// updateExternal 改名 / 换链接。
//
// 两个字段都可省略（nil = 不动）。换链接必须能解析，且不能和别的节点重复——
// 否则「改名 + 粘一条已存在的链接」就能绕过导入时的去重，凭空造出重复节点。
func (a *Server) updateExternal(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	cur, err := a.Store.GetExternalNode(id)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if cur == nil {
		fail(w, 404, "外部节点不存在")
		return
	}
	var req struct {
		Name    *string `json:"name"`
		RawLink *string `json:"raw_link"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			fail(w, 400, "名称不能为空")
			return
		}
		// 名称在客户端配置里就是 proxy tag：太长没意义，还会挤爆界面
		if len([]rune(name)) > 64 {
			fail(w, 400, "名称过长（最多 64 个字）")
			return
		}
		cur.Name = name
	}
	if req.RawLink != nil {
		raw := strings.TrimSpace(*req.RawLink)
		node, perr := link.Parse(raw)
		if perr != nil {
			fail(w, 400, "链接无法解析: "+perr.Error())
			return
		}
		if raw != cur.RawLink {
			ix, ierr := a.newExtIndex(cur.ID)
			if ierr != nil {
				fail(w, 500, ierr.Error())
				return
			}
			if why := ix.dup(node); why != "" {
				fail(w, 409, why)
				return
			}
		}
		cur.RawLink = raw
	}
	if err := a.Store.UpdateExternalNode(cur); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditExternal, cur.Name, fmt.Sprintf("修改外部节点「%s」", cur.Name))
	writeJSON(w, 200, extView{ExternalNode: cur, Parsed: describeExternal(cur.RawLink)})
}

func (a *Server) deleteExternal(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if err := a.Store.DeleteExternalNode(id); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditExternal, fmt.Sprintf("#%d", id), fmt.Sprintf("删除外部节点 #%d", id))
	writeJSON(w, 200, map[string]string{"ok": "1"})
}
