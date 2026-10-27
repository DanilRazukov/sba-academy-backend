package main

import (
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	l := newRateLimiter(2, time.Minute)
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	if ok, _ := l.allow("a", t0); !ok {
		t.Fatal("1-я попытка отклонена")
	}
	if ok, _ := l.allow("a", t0.Add(10*time.Second)); !ok {
		t.Fatal("2-я попытка отклонена")
	}
	ok, retry := l.allow("a", t0.Add(20*time.Second))
	if ok {
		t.Fatal("3-я попытка в окне принята")
	}
	if retry != 40*time.Second {
		t.Errorf("retryAfter = %v, want 40s", retry)
	}

	if ok, _ := l.allow("b", t0.Add(20*time.Second)); !ok {
		t.Error("другой ключ не должен страдать от лимита первого")
	}

	// первая попытка вышла из окна — место освободилось
	if ok, _ := l.allow("a", t0.Add(61*time.Second)); !ok {
		t.Error("после окна попытка отклонена")
	}
}

func TestRateLimiterSweep(t *testing.T) {
	l := newRateLimiter(5, time.Minute)
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	l.allow("old", t0)
	l.allow("fresh", t0.Add(2*time.Minute)) // окно прошло — запускается чистка
	if _, ok := l.hits["old"]; ok {
		t.Error("ключ без свежих попыток не удалён")
	}
	if _, ok := l.hits["fresh"]; !ok {
		t.Error("свежий ключ удалён")
	}
}
