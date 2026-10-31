package api

// 配置 diff：对比「当前运行配置」与「按当前意图重新渲染的配置」，
// 输出行级 unified diff，供前端在应用前确认变更范围。
// 实现为纯标准库的 LCS 行 diff（配置文件规模在几千行内，O(n²) 足够）。

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

type diffOp struct {
	Op    string `json:"op"` // " " | "+" | "-"
	OldLn int    `json:"old_ln,omitempty"`
	NewLn int    `json:"new_ln,omitempty"`
	Text  string `json:"text"`
}

// POST /api/config/diff {"core":"singbox"}
func (a *Server) handleConfigDiff(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Core string `json:"core"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	input, err := a.buildRenderInput(req.Core)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	_, newContent, err := a.Pipe.DryRun(req.Core, input)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	oldContent := ""
	oldMissing := true
	if p, _ := a.Pipe.ConfigPath(req.Core); p != "" {
		if data, rerr := os.ReadFile(p); rerr == nil {
			oldContent = string(data)
			oldMissing = false
		}
	}
	ops := lineDiff(strings.Split(oldContent, "\n"), strings.Split(newContent, "\n"))
	changed := 0
	for _, o := range ops {
		if o.Op != " " {
			changed++
		}
	}
	writeJSON(w, 200, map[string]any{
		"core":        req.Core,
		"old_missing": oldMissing,
		"changed":     changed,
		"diff":        ops,
		"new_hash":    shortHash([]byte(newContent)),
	})
}

// lineDiff 计算两段文本的行级差异（LCS）。
// 空旧内容（首次生成）时全部标记为新增。
func lineDiff(oldLines, newLines []string) []diffOp {
	n, m := len(oldLines), len(newLines)
	if n == 1 && oldLines[0] == "" {
		oldLines = nil
		n = 0
	}
	if n == 0 {
		out := make([]diffOp, 0, m)
		for i, l := range newLines {
			out = append(out, diffOp{Op: "+", NewLn: i + 1, Text: l})
		}
		return out
	}
	// dp[i][j] = old[i:] 与 new[j:] 的 LCS 长度
	dp := make([][]int32, n+1)
	for i := range dp {
		dp[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	out := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			out = append(out, diffOp{Op: " ", OldLn: i + 1, NewLn: j + 1, Text: oldLines[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			out = append(out, diffOp{Op: "-", OldLn: i + 1, Text: oldLines[i]})
			i++
		default:
			out = append(out, diffOp{Op: "+", NewLn: j + 1, Text: newLines[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffOp{Op: "-", OldLn: i + 1, Text: oldLines[i]})
	}
	for ; j < m; j++ {
		out = append(out, diffOp{Op: "+", NewLn: j + 1, Text: newLines[j]})
	}
	return out
}
