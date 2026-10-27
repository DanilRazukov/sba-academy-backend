package main

import (
	"sync"
	"time"
)

// rateLimiter — не больше max заявок за скользящее окно window с одного ключа (IP).
// Состояние в памяти: при нескольких экземплярах сервиса у каждого свой счётчик.
type rateLimiter struct {
	max    int
	window time.Duration

	mu        sync.Mutex
	hits      map[string][]time.Time
	lastSweep time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window, hits: map[string][]time.Time{}}
}

// allow учитывает попытку и говорит, можно ли её принять. Если нельзя —
// retryAfter: через сколько освободится место.
func (l *rateLimiter) allow(key string, now time.Time) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) > l.window {
		l.sweep(now)
	}

	recent := l.prune(l.hits[key], now)
	if len(recent) >= l.max {
		l.hits[key] = recent
		return false, recent[0].Add(l.window).Sub(now)
	}
	l.hits[key] = append(recent, now)
	return true, 0
}

// prune оставляет попытки внутри окна (слайс отсортирован по времени).
func (l *rateLimiter) prune(hits []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-l.window)
	i := 0
	for i < len(hits) && !hits[i].After(cutoff) {
		i++
	}
	return hits[i:]
}

// sweep убирает ключи без свежих попыток, чтобы мапа не росла бесконечно.
func (l *rateLimiter) sweep(now time.Time) {
	for key, hits := range l.hits {
		if recent := l.prune(hits, now); len(recent) == 0 {
			delete(l.hits, key)
		} else {
			l.hits[key] = recent
		}
	}
	l.lastSweep = now
}
