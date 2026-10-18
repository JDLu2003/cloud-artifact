package auth

import (
	"sync"
	"time"
)

// Limiter 是按键（Key ID、用户、IP）计数的令牌桶。
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewLimiter() *Limiter {
	l := &Limiter{buckets: map[string]*bucket{}}
	go l.gc()
	return l
}

// Allow 判断 key 在每分钟 perMin 次的速率下是否还能通过；突发上限等于 perMin。
func (l *Limiter) Allow(key string, perMin int64) (bool, time.Duration) {
	if perMin <= 0 {
		return true, 0
	}
	now := time.Now()
	rate := float64(perMin) / 60.0
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(perMin), last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * rate
	if b.tokens > float64(perMin) {
		b.tokens = float64(perMin)
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / rate * float64(time.Second))
	return false, wait
}

func (l *Limiter) gc() {
	t := time.NewTicker(5 * time.Minute)
	for range t.C {
		cut := time.Now().Add(-30 * time.Minute)
		l.mu.Lock()
		for k, b := range l.buckets {
			if b.last.Before(cut) {
				delete(l.buckets, k)
			}
		}
		l.mu.Unlock()
	}
}

// Blocked 判断 key 的令牌是否已经用完（不消耗令牌）。
func (l *Limiter) Blocked(key string, perMin int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		return false
	}
	tokens := b.tokens + time.Since(b.last).Seconds()*float64(perMin)/60.0
	return tokens < 1
}
