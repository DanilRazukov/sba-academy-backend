package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DanilRazukov/sba-academy-backend/appmail"
)

// minFillTime — порог защиты от ботов, как MIN_FILL_TIME_MS на фронте.
const minFillTime = 3 * time.Second

// maxBodyBytes — заявка из четырёх полей весит меньше килобайта.
const maxBodyBytes = 16 << 10

// submitRequest — тело POST /applications: поля заявки и, отдельно от них,
// служебные данные против ботов (ApplicationBotCheck на фронте).
type submitRequest struct {
	Fields   applicationFields `json:"fields"`
	BotCheck struct {
		Trap       string  `json:"trap"`
		FillTimeMs float64 `json:"fillTimeMs"`
	} `json:"botCheck"`
}

// submitResponse — SubmitApplicationResult на фронте.
type submitResponse struct {
	OK            bool              `json:"ok"`
	ApplicationID string            `json:"applicationId,omitempty"`
	Code          string            `json:"code,omitempty"`
	FieldErrors   map[string]string `json:"fieldErrors,omitempty"`
}

type server struct {
	notifiers   []notifier
	limiter     *rateLimiter
	logger      *slog.Logger
	trustProxy  bool
	sendTimeout time.Duration
	now         func() time.Time

	// inflight — каналы, которые ещё досылают заявку после ответа клиенту;
	// при остановке сервиса их ждёт waitInflight.
	inflight sync.WaitGroup
}

func (s *server) routes(allowedOrigins []string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /applications", s.handleSubmit)
	return withCORS(allowedOrigins, mux)
}

func (s *server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var req submitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, submitResponse{Code: "server"})
		return
	}

	now := s.now()
	ip := clientIP(r, s.trustProxy)
	if ok, retryAfter := s.limiter.allow(ip, now); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
		writeJSON(w, http.StatusTooManyRequests, submitResponse{Code: "rate-limited"})
		return
	}

	id := newApplicationID(now)

	// Бота отбрасываем молча и отвечаем как успешной заявке, чтобы он не подбирал обход.
	fillTime := time.Duration(req.BotCheck.FillTimeMs * float64(time.Millisecond))
	if req.BotCheck.Trap != "" || fillTime < minFillTime {
		s.logger.Info("bot rejected", "id", id, "trap", req.BotCheck.Trap != "", "fillTimeMs", req.BotCheck.FillTimeMs)
		writeJSON(w, http.StatusCreated, submitResponse{OK: true, ApplicationID: id})
		return
	}

	fields := req.Fields.trimmed()
	if errs := validateFields(fields); len(errs) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, submitResponse{Code: "validation", FieldErrors: errs})
		return
	}

	app := appmail.Application{
		ID:         id,
		CourseID:   fields.CourseID,
		Name:       fields.Name,
		Phone:      fields.Phone,
		Email:      fields.Email,
		ReceivedAt: now,
	}

	if !s.deliver(app) {
		s.logger.Error("application lost: all channels failed", "id", id)
		writeJSON(w, http.StatusBadGateway, submitResponse{Code: "server"})
		return
	}

	s.logger.Info("application accepted", "id", id, "course", app.CourseID)
	writeJSON(w, http.StatusCreated, submitResponse{OK: true, ApplicationID: id})
}

// deliver шлёт заявку во все каналы параллельно и возвращает true, как только
// хоть один её доставил, — клиент не ждёт медленный канал. Остальные каналы
// доделывают работу в фоне (сбой только логируем). false — не смог ни один.
//
// Контекст не связан с запросом: клиент мог закрыть вкладку, а заявку всё
// равно нужно довести до конца — в пределах sendTimeout.
func (s *server) deliver(app appmail.Application) bool {
	ctx, cancel := context.WithTimeout(context.Background(), s.sendTimeout)

	// буфер на все каналы: опоздавшие пишут результат, даже когда его уже никто не читает
	results := make(chan bool, len(s.notifiers))
	var wg sync.WaitGroup
	for _, n := range s.notifiers {
		wg.Add(1)
		s.inflight.Add(1)
		go func() {
			defer s.inflight.Done()
			defer wg.Done()
			err := n.Notify(ctx, app)
			if err != nil {
				s.logger.Warn("channel failed", "id", app.ID, "channel", n.Name(), "err", err)
			}
			results <- err == nil
		}()
	}
	// контекст живёт, пока не закончат все каналы, а не до ответа клиенту
	go func() {
		wg.Wait()
		cancel()
	}()

	for range s.notifiers {
		if <-results {
			return true
		}
	}
	return false
}

// waitInflight ждёт каналы, которые ещё досылают заявки, но не дольше ctx.
func (s *server) waitInflight(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// clientIP — ключ для ограничения частоты. За прокси берём последний адрес
// из X-Forwarded-For: его дописал наш прокси, подделать его клиент не может.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// newApplicationID — «A-261028-3f9a1c»: дата по Москве и случайный хвост.
func newApplicationID(now time.Time) string {
	return "A-" + now.In(moscow).Format("060102") + "-" + randomHex(3)
}

// withCORS разрешает браузеру слать заявки с лендинга (другой origin).
func withCORS(allowed []string, next http.Handler) http.Handler {
	anyOrigin := slices.Contains(allowed, "*")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Add("Vary", "Origin")
			if anyOrigin || slices.Contains(allowed, origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
