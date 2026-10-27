package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DanilRazukov/sba-academy-backend/appmail"
)

// fakeNotifier запоминает заявки и может притвориться сломанным или медленным.
type fakeNotifier struct {
	name  string
	err   error
	delay time.Duration

	mu   sync.Mutex
	apps []appmail.Application
}

func (f *fakeNotifier) Name() string { return f.name }

func (f *fakeNotifier) Notify(ctx context.Context, app appmail.Application) error {
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps = append(f.apps, app)
	return f.err
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.apps)
}

var testNow = time.Date(2026, 10, 28, 11, 5, 0, 0, time.UTC)

func newTestServer(notifiers ...notifier) *server {
	return &server{
		notifiers:   notifiers,
		limiter:     newRateLimiter(3, 10*time.Minute),
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		sendTimeout: time.Second,
		now:         func() time.Time { return testNow },
	}
}

const validBody = `{
	"fields": {"courseId": "crypto-assets", "name": " Анна ", "phone": "+7 900 123-45-67", "email": "anna@mail.ru"},
	"botCheck": {"trap": "", "fillTimeMs": 8500}
}`

func post(t *testing.T, h http.Handler, body string, mutate ...func(*http.Request)) (*httptest.ResponseRecorder, submitResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/applications", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://academysba.pro")
	for _, m := range mutate {
		m(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var resp submitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("ответ не JSON (%d): %q", rec.Code, rec.Body.String())
	}
	return rec, resp
}

// waitInflight дожидается каналов, которые досылают заявку после ответа.
func waitInflight(t *testing.T, s *server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.waitInflight(ctx); err != nil {
		t.Fatalf("фоновые каналы не закончили: %v", err)
	}
}

func TestSubmitAccepted(t *testing.T) {
	tg, mail := &fakeNotifier{name: "telegram"}, &fakeNotifier{name: "email"}
	s := newTestServer(tg, mail)
	h := s.routes([]string{"https://academysba.pro"})

	rec, resp := post(t, h, validBody)
	waitInflight(t, s)

	if rec.Code != http.StatusCreated || !resp.OK {
		t.Fatalf("status %d, resp %+v", rec.Code, resp)
	}
	if !strings.HasPrefix(resp.ApplicationID, "A-261028-") {
		t.Errorf("applicationId = %q", resp.ApplicationID)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://academysba.pro" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
	if tg.count() != 1 || mail.count() != 1 {
		t.Fatalf("доставлено telegram=%d email=%d", tg.count(), mail.count())
	}
	app := tg.apps[0]
	if app.Name != "Анна" || app.ID != resp.ApplicationID || !app.ReceivedAt.Equal(testNow) {
		t.Errorf("заявка в канале: %+v", app)
	}
}

func TestSubmitBotsGetFakeSuccess(t *testing.T) {
	for name, body := range map[string]string{
		"ловушка заполнена": strings.Replace(validBody, `"trap": ""`, `"trap": "http://spam"`, 1),
		"слишком быстро":    strings.Replace(validBody, `8500`, `1200`, 1),
		"нет botCheck":      `{"fields": {"courseId": "crypto-assets", "name": "Анна", "phone": "+79001234567", "email": "a@b.ru"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			n := &fakeNotifier{name: "telegram"}
			rec, resp := post(t, newTestServer(n).routes([]string{"*"}), body)
			if rec.Code != http.StatusCreated || !resp.OK || resp.ApplicationID == "" {
				t.Errorf("бот должен получить успех: %d %+v", rec.Code, resp)
			}
			if n.count() != 0 {
				t.Error("заявка бота ушла менеджерам")
			}
		})
	}
}

func TestSubmitValidationError(t *testing.T) {
	n := &fakeNotifier{name: "telegram"}
	body := strings.Replace(validBody, `"anna@mail.ru"`, `"anna@"`, 1)
	rec, resp := post(t, newTestServer(n).routes([]string{"*"}), body)

	if rec.Code != http.StatusUnprocessableEntity || resp.OK || resp.Code != "validation" {
		t.Fatalf("status %d, resp %+v", rec.Code, resp)
	}
	if resp.FieldErrors["email"] == "" || len(resp.FieldErrors) != 1 {
		t.Errorf("fieldErrors = %v", resp.FieldErrors)
	}
	if n.count() != 0 {
		t.Error("невалидная заявка ушла менеджерам")
	}
}

func TestSubmitBadRequest(t *testing.T) {
	h := newTestServer(&fakeNotifier{name: "telegram"}).routes([]string{"*"})

	rec, resp := post(t, h, `{bad json`)
	if rec.Code != http.StatusBadRequest || resp.Code != "server" {
		t.Errorf("битый JSON: %d %+v", rec.Code, resp)
	}

	huge := `{"fields": {"name": "` + strings.Repeat("я", maxBodyBytes) + `"}}`
	rec, resp = post(t, h, huge)
	if rec.Code != http.StatusRequestEntityTooLarge || resp.Code != "server" {
		t.Errorf("большое тело: %d %+v", rec.Code, resp)
	}
}

func TestSubmitDelivery(t *testing.T) {
	broken := errors.New("boom")
	tests := []struct {
		name       string
		tgErr      error
		mailErr    error
		wantStatus int
		wantOK     bool
	}{
		{"оба канала работают", nil, nil, http.StatusCreated, true},
		{"сломан telegram", broken, nil, http.StatusCreated, true},
		{"сломана почта", nil, broken, http.StatusCreated, true},
		{"сломаны оба", broken, broken, http.StatusBadGateway, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServer(&fakeNotifier{name: "telegram", err: tt.tgErr}, &fakeNotifier{name: "email", err: tt.mailErr})
			rec, resp := post(t, s.routes([]string{"*"}), validBody)
			if rec.Code != tt.wantStatus || resp.OK != tt.wantOK {
				t.Errorf("status %d, resp %+v", rec.Code, resp)
			}
			if !tt.wantOK && resp.Code != "server" {
				t.Errorf("code = %q, want server", resp.Code)
			}
		})
	}
}

func TestSubmitRespondsOnFirstSuccess(t *testing.T) {
	fast := &fakeNotifier{name: "telegram"}
	slow := &fakeNotifier{name: "email", delay: 500 * time.Millisecond}
	s := newTestServer(fast, slow)

	start := time.Now()
	rec, resp := post(t, s.routes([]string{"*"}), validBody)
	elapsed := time.Since(start)

	if rec.Code != http.StatusCreated || !resp.OK {
		t.Fatalf("status %d, resp %+v", rec.Code, resp)
	}
	if elapsed >= slow.delay {
		t.Errorf("ответ ждал медленный канал: %v", elapsed)
	}
	if slow.count() != 0 {
		t.Fatal("медленный канал успел раньше ответа — тест ничего не проверяет")
	}

	waitInflight(t, s)
	if slow.count() != 1 {
		t.Error("медленный канал не дослал заявку после ответа")
	}
}

func TestSubmitWaitsForLateSuccess(t *testing.T) {
	// быстрый канал падает — ответ ждёт, пока медленный не доставит
	broken := &fakeNotifier{name: "telegram", err: errors.New("boom")}
	slow := &fakeNotifier{name: "email", delay: 100 * time.Millisecond}
	s := newTestServer(broken, slow)

	rec, resp := post(t, s.routes([]string{"*"}), validBody)
	if rec.Code != http.StatusCreated || !resp.OK {
		t.Errorf("status %d, resp %+v", rec.Code, resp)
	}
	if slow.count() != 1 {
		t.Error("успех засчитан до доставки")
	}
}

func TestWaitInflightRespectsDeadline(t *testing.T) {
	slow := &fakeNotifier{name: "email", delay: time.Second}
	s := newTestServer(&fakeNotifier{name: "telegram"}, slow)
	post(t, s.routes([]string{"*"}), validBody)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.waitInflight(ctx); err == nil {
		t.Error("waitInflight должен сдаться по дедлайну, пока канал ещё шлёт")
	}
	waitInflight(t, s) // дождаться, чтобы горутина не пережила тест
}

func TestSubmitRateLimited(t *testing.T) {
	n := &fakeNotifier{name: "telegram"}
	h := newTestServer(n).routes([]string{"*"})

	for i := range 3 {
		if rec, _ := post(t, h, validBody); rec.Code != http.StatusCreated {
			t.Fatalf("попытка %d: %d", i+1, rec.Code)
		}
	}
	rec, resp := post(t, h, validBody)
	if rec.Code != http.StatusTooManyRequests || resp.Code != "rate-limited" {
		t.Fatalf("4-я попытка: %d %+v", rec.Code, resp)
	}
	if rec.Header().Get("Retry-After") != "600" {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}

	// другой IP не страдает
	rec, _ = post(t, h, validBody, func(r *http.Request) { r.RemoteAddr = "203.0.113.9:5555" })
	if rec.Code != http.StatusCreated {
		t.Errorf("другой IP: %d", rec.Code)
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "10.0.0.2:4444"
	r.Header.Set("X-Forwarded-For", "1.1.1.1, 198.51.100.7")

	if got := clientIP(r, false); got != "10.0.0.2" {
		t.Errorf("без прокси: %q", got)
	}
	if got := clientIP(r, true); got != "198.51.100.7" {
		t.Errorf("за прокси: %q — нужен последний адрес, первый клиент может подделать", got)
	}
}

func TestCORS(t *testing.T) {
	h := newTestServer(&fakeNotifier{name: "telegram"}).routes([]string{"https://academysba.pro"})

	preflight := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodOptions, "/applications", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := preflight("https://academysba.pro")
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://academysba.pro" {
		t.Errorf("свой origin: %d %v", rec.Code, rec.Header())
	}
	if rec.Header().Get("Access-Control-Allow-Headers") != "Content-Type" {
		t.Errorf("Allow-Headers = %q", rec.Header().Get("Access-Control-Allow-Headers"))
	}

	rec = preflight("https://evil.example")
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("чужой origin получил разрешение")
	}
}

func TestHealth(t *testing.T) {
	h := newTestServer().routes([]string{"*"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Errorf("health: %d %q", rec.Code, rec.Body.String())
	}
}
