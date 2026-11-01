package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestRealityKeypairRoundTrip 生成 → 由私钥推导公钥，两者必须一致。
// 这条路径不依赖内核二进制：Reality 是标准 X25519，纯 Go 就能算。
func TestRealityKeypairRoundTrip(t *testing.T) {
	a := newAuthTestServer(t)
	h := a.Handler()
	tok := loginForToken(t, a)

	w := apiReq(t, h, tok, "POST", "/api/tools/reality-keypair", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("生成密钥对应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var gen struct {
		PrivateKey string `json:"private_key"`
		PublicKey  string `json:"public_key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &gen); err != nil {
		t.Fatal(err)
	}
	if len(gen.PrivateKey) != 43 || len(gen.PublicKey) != 43 {
		t.Fatalf("base64url 私钥/公钥应各 43 字符，实际 %d/%d", len(gen.PrivateKey), len(gen.PublicKey))
	}

	// 用私钥再推一次：必须得到同一个公钥
	w2 := apiReq(t, h, tok, "POST", "/api/tools/reality-keypair",
		`{"private_key":"`+gen.PrivateKey+`"}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("推导公钥应 200，实际 %d: %s", w2.Code, w2.Body.String())
	}
	var got struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.PublicKey != gen.PublicKey {
		t.Errorf("由私钥推导的公钥不一致：%s vs %s", got.PublicKey, gen.PublicKey)
	}

	// 不合法私钥要明确报错，而不是给一个看起来正常的空公钥
	if bad := apiReq(t, h, tok, "POST", "/api/tools/reality-keypair", `{"private_key":"abc"}`); bad.Code != 400 {
		t.Errorf("非法私钥应 400，实际 %d: %s", bad.Code, bad.Body.String())
	}
}
