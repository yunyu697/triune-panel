package api

import (
	"os"
	"testing"

	"triune/internal/authn"
)

// TestControlUIDAllowed 谁能用控制套接字：root，或者面板自己的运行账号。
//
// 后者是为了非 root 安装（TRIUNE_INSTALL_DIR=~/triune-panel）：那种情况下
// 面板和用户是同一个人，要求 sudo 只会逼人多绕一步。
func TestControlUIDAllowed(t *testing.T) {
	if !controlUIDAllowed(0) {
		t.Error("root 应该被允许")
	}
	if !controlUIDAllowed(os.Geteuid()) {
		t.Errorf("面板运行账号 uid=%d 应该被允许", os.Geteuid())
	}
	// 换一个一定不是自己的 uid
	other := os.Geteuid() + 1
	if other == 0 {
		other = 12345
	}
	if controlUIDAllowed(other) {
		t.Errorf("uid=%d 不是 root 也不是面板账号，不该被允许", other)
	}
}

// TestControlUserSyntheticIdentity 合成身份的命名要能一眼看出「这是命令行」。
//
// 不借用真实 owner 账号：审计里必须分得清「面板上点的」和「服务器上敲的」，
// 两者长得一样的话，出事时查不出是谁做的。
func TestControlUserSyntheticIdentity(t *testing.T) {
	u := controlUser(0)
	if u.Username != "cli@root" {
		t.Errorf("root 调用的操作人应为 cli@root，得到 %q", u.Username)
	}
	if u.Role != authn.RoleOwner {
		t.Errorf("命令行应当按 owner 对待，得到 %q", u.Role)
	}
	if !u.Enabled {
		t.Error("合成账号必须 enabled，否则角色判定会被当成停用账号")
	}

	v := controlUser(1000)
	if v.Username != "cli:uid1000" {
		t.Errorf("非 root 应带 uid，得到 %q", v.Username)
	}
}
