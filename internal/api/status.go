package api

import (
	"net/http"

	"triune/internal/core"
)

// ---- status ----

func (a *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	type coreStatus struct {
		Core       string `json:"core"`
		BinSet     bool   `json:"bin_set"`
		BinFound   bool   `json:"bin_found"`
		Configured bool   `json:"config_exists"`
		Running    bool   `json:"running"`
	}
	var out []coreStatus
	for _, c := range core.All() {
		cs := coreStatus{Core: c.Name()}
		bin, _ := a.Store.GetSetting("bin_" + c.Name())
		if bin != "" {
			cs.BinSet = true
			cs.BinFound = fileExists(bin)
		}
		if p, _ := a.Pipe.ConfigPath(c.Name()); p != "" && fileExists(p) {
			cs.Configured = true
		}
		cs.Running, _ = a.ProcMgr.Status(c.Name())
		out = append(out, cs)
	}
	writeJSON(w, 200, map[string]any{"cores": out})
}

func fileExists(p string) bool {
	st, err := statFile(p)
	return err == nil && !st.IsDir()
}
