// Package auth 负责密钥、密码、会话令牌、签名链接和限流。
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const KeyPrefix = "art_"

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// RandomString 生成指定长度的 base62 随机串。
func RandomString(n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(base62)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b[i] = base62[v.Int64()]
	}
	return string(b)
}

// NewAPIKey 返回明文密钥、展示用前缀和存储用哈希。
func NewAPIKey() (plain, prefix, hash string) {
	plain = KeyPrefix + RandomString(36)
	prefix = plain[:len(KeyPrefix)+6]
	return plain, prefix, HashToken(plain)
}

func LooksLikeKey(s string) bool {
	return strings.HasPrefix(s, KeyPrefix) && len(s) >= len(KeyPrefix)+20 && len(s) <= 100
}

// HashToken 对高熵令牌做 sha256，无需加盐。
func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func NewSessionToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func HashPassword(pw string) (string, error) {
	if err := ValidatePassword(pw); err != nil {
		return "", err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 11)
	return string(h), err
}

func ValidatePassword(pw string) error {
	if len(pw) < 8 {
		return errors.New("密码至少 8 位")
	}
	if len(pw) > 72 {
		return errors.New("密码不能超过 72 字节")
	}
	return nil
}

var (
	dummyOnce sync.Once
	dummy     []byte
)

func dummyHash() []byte {
	dummyOnce.Do(func() { dummy, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), 11) })
	return dummy
}

func CheckPassword(hash, pw string) bool {
	if hash == "" {
		// 仍然做一次比较，避免通过耗时判断用户是否存在
		bcrypt.CompareHashAndPassword(dummyHash(), []byte(pw))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// Signer 生成和校验内容访问令牌，令牌放在 URL 路径里，相对路径资源会自动带上。
type Signer struct {
	key []byte
}

func NewSigner(secret string) *Signer {
	return &Signer{key: []byte(secret)}
}

func (s *Signer) mac(parts ...string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(strings.Join(parts, "|")))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:15])
}

// ContentToken 生成访问 artifact 某个版本的令牌，形如 <过期时间>.<签名>。
func (s *Signer) ContentToken(id string, version int, ttl time.Duration) string {
	exp := strconv.FormatInt(time.Now().Add(ttl).Unix(), 36)
	return exp + "." + s.mac("c", id, strconv.Itoa(version), exp)
}

func (s *Signer) VerifyContentToken(tok, id string, version int) bool {
	exp, sig, ok := strings.Cut(tok, ".")
	if !ok {
		return false
	}
	t, err := strconv.ParseInt(exp, 36, 64)
	if err != nil || time.Now().Unix() > t {
		return false
	}
	want := s.mac("c", id, strconv.Itoa(version), exp)
	return subtle.ConstantTimeCompare([]byte(sig), []byte(want)) == 1
}

// CSRFToken 由会话令牌派生，供后台写操作校验。
func (s *Signer) CSRFToken(sessionToken string) string {
	return s.mac("csrf", sessionToken)
}

func (s *Signer) VerifyCSRF(sessionToken, tok string) bool {
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.CSRFToken(sessionToken))) == 1
}

func FormatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
