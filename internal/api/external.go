package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"triune/internal/link"
	"triune/internal/model"
)

// ---- external nodes ----

func (a *Server) listExternal(w http.ResponseWriter, r *http.Request) {
	ns, err := a.Store.ListExternalNodes()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if ns == nil {
		ns = []*model.ExternalNode{}
	}
	writeJSON(w, 200, ns)
}

func (a *Server) addExternal(w http.ResponseWriter, r *http.Request) {
	var n model.ExternalNode
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if _, err := link.Parse(n.RawLink); err != nil {
		fail(w, 400, "链接无法解析: "+err.Error())
		return
	}
	if err := a.Store.CreateExternalNode(&n); err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.auditOK(r, AuditExternal, n.Name, "新增外部节点")
	writeJSON(w, 200, n)
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
