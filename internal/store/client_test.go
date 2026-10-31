package store

import (
	"testing"
	"time"

	"triune/internal/model"
)

// TestClientCRUDRoundTrip 用户的所有字段必须能原样存取。
//
// 这条守的是「SQL 列清单与 scanClient 的扫描顺序对不上」——两边都是手写的，
// 改一处忘另一处，字段就会串位（比如 enabled 读到 quota_gb 上），
// 而且不会报错，只是数据静默错乱。
func TestClientCRUDRoundTrip(t *testing.T) {
	s := newTestStore(t)

	exp := time.Date(2027, 3, 4, 5, 6, 7, 0, time.UTC)
	c := &model.Client{
		Name: "alice", Credential: "uuid-1", SubToken: "tok-1",
		QuotaGB: 12.5, ExpiresAt: &exp, Enabled: true,
	}
	if err := s.CreateClient(c); err != nil {
		t.Fatal(err)
	}
	if c.ID == 0 {
		t.Fatal("CreateClient 未回填 ID")
	}

	got, err := s.GetClientByToken("tok-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != c.ID || got.Name != "alice" || got.Credential != "uuid-1" || got.QuotaGB != 12.5 {
		t.Fatalf("读回的字段与写入不一致: %+v", got)
	}
	if !got.Enabled {
		t.Fatalf("enabled 读错: %+v", got)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
		t.Fatalf("expires_at 读错: %v", got.ExpiresAt)
	}

	// 更新后重读：确认 UPDATE 的占位符顺序也对
	got.Name = "alice2"
	got.QuotaGB = 0
	got.ExpiresAt = nil
	got.Enabled = false
	if err := s.UpdateClient(got); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListClients()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("应有 1 个用户，实际 %d", len(list))
	}
	g := list[0]
	if g.Name != "alice2" || g.QuotaGB != 0 || g.Enabled || g.ExpiresAt != nil {
		t.Fatalf("更新后读回不一致: %+v", g)
	}
	if g.Credential != "uuid-1" || g.SubToken != "tok-1" {
		t.Fatalf("更新不应改动凭据: %+v", g)
	}
}

// TestLegacyRateLimitColumnIgnored 旧库里 rate_limit_mbps 有值也不能影响读写。
//
// 该列是历史遗留（三个内核都没有每用户限速能力，见 model.Client 注释），
// 代码已不再读写它；但已发布的库里列还在、可能存着旧值。
// 读写链路必须完全无视它——如果哪天有人把它加回 clientCols 却忘了 scanClient，
// 这条会立刻因为字段串位而失败。
func TestLegacyRateLimitColumnIgnored(t *testing.T) {
	s := newTestStore(t)

	c := &model.Client{Name: "bob", Credential: "uuid-2", SubToken: "tok-2", QuotaGB: 3, Enabled: true}
	if err := s.CreateClient(c); err != nil {
		t.Fatal(err)
	}
	// 模拟旧版本写入的值
	if _, err := s.db.Exec(`UPDATE clients SET rate_limit_mbps=100 WHERE id=?`, c.ID); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetClientByToken("tok-2")
	if err != nil {
		t.Fatalf("旧列有值时读取不应失败: %v", err)
	}
	if got.Name != "bob" || got.QuotaGB != 3 || !got.Enabled {
		t.Fatalf("字段读错: %+v", got)
	}

	got.Name = "bob2"
	if err := s.UpdateClient(got); err != nil {
		t.Fatalf("旧列存在时更新不应失败: %v", err)
	}
	again, err := s.GetClientByToken("tok-2")
	if err != nil || again.Name != "bob2" {
		t.Fatalf("更新后重读失败: %v %+v", err, again)
	}
}
