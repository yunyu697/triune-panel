package api

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveRealitySNI 对着真实目标跑一次收集（默认跳过）。
//
// 单测用的是自签证书，验不了「真实 CDN 的证书长什么样」——比如一张证书里
// 塞 28 个 SAN、或者只有通配符。要跑：
//
//	TRIUNE_LIVE_DEST=www.amd.com go test ./internal/api/ -run TestLiveRealitySNI -v
//
// 只读、不出网以外的副作用，可以放心对着任何大站跑。
func TestLiveRealitySNI(t *testing.T) {
	host := strings.TrimSpace(os.Getenv("TRIUNE_LIVE_DEST"))
	if host == "" {
		t.Skip("设置 TRIUNE_LIVE_DEST=<域名> 才跑真实目标")
	}
	cands, msg := normalizeDestCandidates([]destCandidate{{Host: host, Port: 443}})
	if msg != "" {
		t.Fatal(msg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	res, errMsg := collectRealitySNI(ctx, cands[0], nil)
	if errMsg != "" {
		t.Fatalf("收集失败：%s", errMsg)
	}
	t.Logf("证书：issuer=%s 到期=%s（%d 天）具体域名=%v 通配符=%v",
		res.Cert.Issuer, res.Cert.NotAfter, res.Cert.DaysLeft, res.Cert.Names, res.Cert.Wildcard)
	t.Logf("共试 %d 个，验过 %d 个：", res.Tried, len(res.SNIs))
	for _, s := range res.SNIs {
		t.Logf("  ✓ %-32s %4d ms  (%s)", s.SNI, s.MS, s.Source)
	}
	for _, r := range res.Rejected {
		t.Logf("  ✗ %-32s %s", r.SNI, r.Detail)
	}
	if len(res.SNIs) == 0 {
		t.Fatalf("一个 SNI 都没验过（note: %s）", res.Note)
	}
}
