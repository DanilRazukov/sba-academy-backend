package main

import (
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"
)

// config — настройки сервиса, все из переменных окружения (см. .env.example).
type config struct {
	Addr string
	// AllowedOrigins — origin'ы лендинга для CORS; "*" разрешает любой.
	AllowedOrigins []string
	// TrustProxy — брать IP клиента из X-Forwarded-For (сервис за nginx/балансировщиком).
	TrustProxy bool
	RateLimit  int
	RateWindow time.Duration

	Telegram telegramConfig
	SMTP     smtpConfig
}

type telegramConfig struct {
	BotToken string
	ChatID   string
	APIBase  string
}

func (c telegramConfig) enabled() bool { return c.BotToken != "" || c.ChatID != "" }

type smtpConfig struct {
	Host string
	Port int
	// Username — логин и он же адрес отправителя: Gmail и Яндекс не дают
	// слать от чужого адреса.
	Username string
	Password string
	// From — «SBA Academy <Username>», собирается из Username.
	From string
	// To — получатели заявок (менеджеры).
	To []string
}

func (c smtpConfig) enabled() bool { return c.Host != "" }

// smtpSenderName — имя отправителя в письмах менеджерам.
const smtpSenderName = "SBA Academy"

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		Addr:           ":" + valueOr(getenv("PORT"), "8080"),
		AllowedOrigins: splitList(getenv("ALLOWED_ORIGINS")),
		Telegram: telegramConfig{
			BotToken: strings.TrimSpace(getenv("TELEGRAM_BOT_TOKEN")),
			ChatID:   strings.TrimSpace(getenv("TELEGRAM_CHAT_ID")),
			APIBase:  valueOr(getenv("TELEGRAM_API_BASE"), "https://api.telegram.org"),
		},
		SMTP: smtpConfig{
			Host:     strings.TrimSpace(getenv("SMTP_HOST")),
			Username: strings.TrimSpace(getenv("SMTP_USERNAME")),
			Password: getenv("SMTP_PASSWORD"),
			To:       splitList(getenv("SMTP_TO")),
		},
	}

	var errs []error

	var err error
	if cfg.TrustProxy, err = parseBool(getenv("TRUST_PROXY"), false); err != nil {
		errs = append(errs, fmt.Errorf("TRUST_PROXY: %w", err))
	}
	if cfg.RateLimit, err = strconv.Atoi(valueOr(getenv("RATE_LIMIT_MAX"), "5")); err != nil || cfg.RateLimit < 1 {
		errs = append(errs, errors.New("RATE_LIMIT_MAX: нужно целое число ≥ 1"))
	}
	if cfg.RateWindow, err = time.ParseDuration(valueOr(getenv("RATE_LIMIT_WINDOW"), "10m")); err != nil || cfg.RateWindow <= 0 {
		errs = append(errs, errors.New("RATE_LIMIT_WINDOW: нужна длительность, например 10m"))
	}

	if len(cfg.AllowedOrigins) == 0 {
		errs = append(errs, errors.New("ALLOWED_ORIGINS: укажите origin лендинга, например https://academysba.pro"))
	}

	if cfg.Telegram.enabled() && (cfg.Telegram.BotToken == "" || cfg.Telegram.ChatID == "") {
		errs = append(errs, errors.New("Telegram: нужны и TELEGRAM_BOT_TOKEN, и TELEGRAM_CHAT_ID"))
	}

	if cfg.SMTP.enabled() {
		if cfg.SMTP.Port, err = strconv.Atoi(valueOr(getenv("SMTP_PORT"), "587")); err != nil {
			errs = append(errs, errors.New("SMTP_PORT: нужно число"))
		}
		if addr, err := mail.ParseAddress(cfg.SMTP.Username); err != nil || addr.Address != cfg.SMTP.Username {
			errs = append(errs, errors.New("SMTP_USERNAME: нужен адрес почты, например noreply@academysba.pro"))
		} else {
			cfg.SMTP.From = (&mail.Address{Name: smtpSenderName, Address: addr.Address}).String()
		}
		if len(cfg.SMTP.To) == 0 {
			errs = append(errs, errors.New("SMTP_TO: нужен хотя бы один получатель"))
		}
		for _, to := range cfg.SMTP.To {
			if _, err := mail.ParseAddress(to); err != nil {
				errs = append(errs, fmt.Errorf("SMTP_TO %q: %w", to, err))
			}
		}
	}

	if !cfg.Telegram.enabled() && !cfg.SMTP.enabled() {
		errs = append(errs, errors.New("не настроен ни один канал: задайте TELEGRAM_* и/или SMTP_*"))
	}

	return cfg, errors.Join(errs...)
}

func valueOr(v, fallback string) string {
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return fallback
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseBool(s string, fallback bool) (bool, error) {
	if s = strings.TrimSpace(s); s == "" {
		return fallback, nil
	}
	return strconv.ParseBool(s)
}
