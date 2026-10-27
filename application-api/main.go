// Сервис приёма заявок с формы «Оставьте заявку» на academysba.pro.
//
// POST /applications принимает заявку (контракт — src/shared/api/application.types.ts),
// проверяет поля и защиту от ботов и рассылает её менеджерам в Telegram и
// на почту (письмо собирает пакет appmail). Настройки — переменные окружения,
// см. .env.example и README.md.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		logger.Error("invalid config", "err", err)
		os.Exit(1)
	}

	var notifiers []notifier
	if cfg.Telegram.enabled() {
		notifiers = append(notifiers, newTelegramNotifier(cfg.Telegram))
	}
	if cfg.SMTP.enabled() {
		notifiers = append(notifiers, &smtpNotifier{cfg: cfg.SMTP})
	}

	s := &server{
		notifiers:   notifiers,
		limiter:     newRateLimiter(cfg.RateLimit, cfg.RateWindow),
		logger:      logger,
		trustProxy:  cfg.TrustProxy,
		sendTimeout: 15 * time.Second,
		now:         time.Now,
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.routes(cfg.AllowedOrigins),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		channels := make([]string, len(notifiers))
		for i, n := range notifiers {
			channels[i] = n.Name()
		}
		logger.Info("listening", "addr", cfg.Addr, "channels", channels, "origins", cfg.AllowedOrigins)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", "err", err)
		os.Exit(1)
	}
	// ответы уже ушли, но медленный канал может ещё досылать заявку
	if err := s.waitInflight(shutdownCtx); err != nil {
		logger.Error("channels still sending at exit", "err", err)
		os.Exit(1)
	}
}
