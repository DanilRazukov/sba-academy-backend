package main

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfig(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"ALLOWED_ORIGINS":    "https://academysba.pro, https://sba-academy.vercel.app",
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"TELEGRAM_CHAT_ID":   "-100500",
		"SMTP_HOST":          "smtp.yandex.ru",
		"SMTP_PORT":          "465",
		"SMTP_USERNAME":      "noreply@academysba.pro",
		"SMTP_TO":            "a@academysba.pro,b@academysba.pro",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" || cfg.RateLimit != 5 || cfg.RateWindow != 10*time.Minute || cfg.TrustProxy {
		t.Errorf("значения по умолчанию: %+v", cfg)
	}
	if len(cfg.AllowedOrigins) != 2 || cfg.AllowedOrigins[1] != "https://sba-academy.vercel.app" {
		t.Errorf("origins = %v", cfg.AllowedOrigins)
	}
	if cfg.SMTP.Port != 465 || len(cfg.SMTP.To) != 2 || cfg.Telegram.APIBase != "https://api.telegram.org" {
		t.Errorf("каналы: %+v %+v", cfg.SMTP, cfg.Telegram)
	}
	if cfg.SMTP.From != `"SBA Academy" <noreply@academysba.pro>` {
		t.Errorf("From = %q", cfg.SMTP.From)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"нет каналов", map[string]string{"ALLOWED_ORIGINS": "*"}, "ни один канал"},
		{"нет origin", map[string]string{"TELEGRAM_BOT_TOKEN": "t", "TELEGRAM_CHAT_ID": "1"}, "ALLOWED_ORIGINS"},
		{"telegram без chat_id", map[string]string{"ALLOWED_ORIGINS": "*", "TELEGRAM_BOT_TOKEN": "t"}, "TELEGRAM_CHAT_ID"},
		{"smtp без получателей", map[string]string{"ALLOWED_ORIGINS": "*", "SMTP_HOST": "h", "SMTP_USERNAME": "a@b.ru"}, "SMTP_TO"},
		{"smtp без логина", map[string]string{"ALLOWED_ORIGINS": "*", "SMTP_HOST": "h", "SMTP_TO": "a@b.ru"}, "SMTP_USERNAME"},
		{"логин не адрес", map[string]string{"ALLOWED_ORIGINS": "*", "SMTP_HOST": "h", "SMTP_USERNAME": "Boss <a@b.ru>", "SMTP_TO": "a@b.ru"}, "SMTP_USERNAME"},
		{"плохое окно", map[string]string{"ALLOWED_ORIGINS": "*", "TELEGRAM_BOT_TOKEN": "t", "TELEGRAM_CHAT_ID": "1", "RATE_LIMIT_WINDOW": "soon"}, "RATE_LIMIT_WINDOW"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadConfig(env(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want содержит %q", err, tt.want)
			}
		})
	}
}
