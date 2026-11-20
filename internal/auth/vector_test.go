package auth

import "testing"

// 这组数据同时写在 worker/test/worker.test.mjs 里：两边算出的令牌必须一致，
// Cloudflare Worker 才能校验服务器签发的内容令牌。
const (
	vectorSecret     = "vector-secret"
	vectorContentKey = "OoWwfmt_62y_XRnKEM7RefQAsVfLjorLkSwCMoYBaJk"
	vectorExp        = "zzzzzzz"
	vectorToken      = "zzzzzzz.axtZUD18vi0tiLsmBpAM"
)

func TestContentTokenVector(t *testing.T) {
	s := NewSigner(vectorSecret)
	key := s.ContentKey()
	tok := vectorExp + "." + macWith(s.contentKey, "c", "abc123", "3", vectorExp)
	t.Logf("key=%s token=%s", key, tok)
	if key != vectorContentKey || tok != vectorToken {
		t.Fatalf("测试向量变了：key=%s token=%s", key, tok)
	}
	if !s.VerifyContentToken(tok, "abc123", 3) || s.VerifyContentToken(tok, "abc123", 4) {
		t.Fatal("校验结果不正确")
	}
}
