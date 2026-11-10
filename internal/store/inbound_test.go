package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"triune/internal/model"
)

// TestInboundNodeHostRoundTrip 入站的客户端地址必须能原样存取。
//
// 与 TestClientCRUDRoundTrip 同一个理由：列清单与 scanInbound 的扫描顺序都是
// 手写的，改一处忘另一处就会串位——而且不报错，只是导出的链接指向别的字段。
func TestInboundNodeHostRoundTrip(t *testing.T) {
	s := newTestStore(t)
	in := &model.Inbound{
		Name: "hk-01", Core: "xray", Protocol: "vless", Port: 443,
		Tag: "hk", Transport: "tcp", TLS: "reality", Settings: "{}",
		NodeHost: "203.0.113.10", Enabled: true,
	}
	if err := s.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInbound(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeHost != "203.0.113.10" {
		t.Fatalf("node_host 读回不一致: %q", got.NodeHost)
	}

	in.NodeHost = "cdn.example.com"
	if err := s.UpdateInbound(in); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetInbound(in.ID); got.NodeHost != "cdn.example.com" {
		t.Fatalf("更新后 node_host = %q", got.NodeHost)
	}

	// 列表走的是另一条 SQL（inboundCols + scanInbound），单独确认一次
	all, err := s.ListInbounds("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].NodeHost != "cdn.example.com" {
		t.Fatalf("ListInbounds 没带上 node_host: %+v", all)
	}
}

// TestMigrateAddsNodeHostToOldDB 老库升级后地址必须是空串（= 跟随全局）。
//
// 这一条守的是「升级不改变现有行为」：如果新列带上了非空默认值，所有老入站
// 导出的链接会在一夜之间换地址，而用户什么都没改。
func TestMigrateAddsNodeHostToOldDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "triune.db")

	// 手工造一个停在 v5 的库：跑前 5 条迁移 + 一条存量入站
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := old.Exec(migrations[i]); err != nil {
			t.Fatalf("预置 v%d 迁移失败: %v", i+1, err)
		}
	}
	if _, err := old.Exec(`INSERT INTO settings(key,value) VALUES('schema_version','5')`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO inbounds(name,core,protocol,port,tag,settings)
		VALUES('old-01','xray','vless',443,'old','{}')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("老库升级失败: %v", err)
	}
	defer s.Close()
	all, err := s.ListInbounds("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("存量入站丢了: %+v", all)
	}
	if all[0].NodeHost != "" {
		t.Errorf("升级后的存量入站 node_host 应为空（跟随全局），实际 %q", all[0].NodeHost)
	}
	if all[0].Name != "old-01" {
		t.Errorf("迁移把别的字段弄坏了: %+v", all[0])
	}
}
