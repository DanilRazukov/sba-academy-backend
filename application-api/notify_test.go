package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DanilRazukov/sba-academy-backend/appmail"
)

var sampleApp = appmail.Application{
	ID:         "A-261028-3f9a1c",
	CourseID:   "crypto-assets",
	Name:       "Анна Комарова",
	Phone:      "+7 (900) 123-45-67",
	Email:      "anna@mail.ru",
	ReceivedAt: time.Date(2026, 10, 28, 11, 5, 0, 0, time.UTC),
}

func TestTelegramText(t *testing.T) {
	text := telegramText(sampleApp)
	for _, want := range []string{
		"№ A-261028-3f9a1c",
		"Программа: Криптовалюта и виртуальные активы (Флагманский курс)",
		"Имя: Анна Комарова",
		"Телефон: +7 (900) 123-45-67",
		"Почта: anna@mail.ru",
		"Получена: 28.10.2026 14:05 МСК",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("нет %q в:\n%s", want, text)
		}
	}

	app := sampleApp
	app.CourseID = "new-course"
	if !strings.Contains(telegramText(app), "Программа: new-course") {
		t.Error("неизвестная программа должна выводиться по id")
	}
}

func TestTelegramNotify(t *testing.T) {
	var got map[string]any
	var path string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["chat_id"] == "bad" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ok":false,"description":"Bad Request: chat not found"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer api.Close()

	tg := newTelegramNotifier(telegramConfig{BotToken: "123:SECRET", ChatID: "-100500", APIBase: api.URL})
	if err := tg.Notify(context.Background(), sampleApp); err != nil {
		t.Fatal(err)
	}
	if path != "/bot123:SECRET/sendMessage" {
		t.Errorf("path = %q", path)
	}
	if got["chat_id"] != "-100500" || !strings.Contains(got["text"].(string), "Анна Комарова") {
		t.Errorf("тело запроса: %v", got)
	}

	tg.cfg.ChatID = "bad"
	err := tg.Notify(context.Background(), sampleApp)
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("ошибка API: %v", err)
	}
}

func TestTelegramErrorHidesToken(t *testing.T) {
	// закрытый порт — запрос падает с url.Error, где есть адрес с токеном
	tg := newTelegramNotifier(telegramConfig{BotToken: "123:SECRET", ChatID: "1", APIBase: "http://127.0.0.1:1"})
	err := tg.Notify(context.Background(), sampleApp)
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("токен попал в ошибку: %v", err)
	}
}

func TestBuildMessage(t *testing.T) {
	msg, err := appmail.Render(sampleApp)
	if err != nil {
		t.Fatal(err)
	}
	from := &mail.Address{Name: "SBA Academy", Address: "noreply@academysba.pro"}
	to := []*mail.Address{{Address: "manager@academysba.pro"}}

	data, err := buildMessage(from, to, msg, sampleApp.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}

	m, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("письмо не разбирается: %v", err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || subject != msg.Subject {
		t.Errorf("Subject = %q (%v), want %q", subject, err, msg.Subject)
	}
	if m.Header.Get("Reply-To") != "<anna@mail.ru>" {
		t.Errorf("Reply-To = %q", m.Header.Get("Reply-To"))
	}
	if !strings.HasSuffix(m.Header.Get("Message-ID"), "@academysba.pro>") {
		t.Errorf("Message-ID = %q", m.Header.Get("Message-ID"))
	}

	mediaType, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("Content-Type: %q %v", mediaType, err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	var types []string
	for {
		p, err := mr.NextPart() // декодирует quoted-printable сам
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(p)
		types = append(types, p.Header.Get("Content-Type"))
		if !strings.Contains(string(body), "Анна Комарова") {
			t.Errorf("в части %s нет имени", p.Header.Get("Content-Type"))
		}
	}
	if strings.Join(types, " | ") != "text/plain; charset=utf-8 | text/html; charset=utf-8" {
		t.Errorf("части: %v", types)
	}
}

// fakeSMTP — минимальный SMTP-сервер без TLS и авторизации: запоминает
// конверт и письмо. Хватает, чтобы прогнать smtpNotifier целиком.
type fakeSMTP struct {
	addr string
	got  chan smtpSession
}

type smtpSession struct {
	from string
	rcpt []string
	data string
}

func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	f := &fakeSMTP{addr: ln.Addr().String(), got: make(chan smtpSession, 1)}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r, w := bufio.NewReader(conn), bufio.NewWriter(conn)
		reply := func(s string) { w.WriteString(s + "\r\n"); w.Flush() }

		var s smtpSession
		reply("220 fake ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.TrimSpace(line)
			switch upper := strings.ToUpper(cmd); {
			case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
				reply("250 fake")
			case strings.HasPrefix(upper, "MAIL FROM:"):
				s.from = strings.Trim(cmd[len("MAIL FROM:"):], "<> ")
				reply("250 ok")
			case strings.HasPrefix(upper, "RCPT TO:"):
				s.rcpt = append(s.rcpt, strings.Trim(cmd[len("RCPT TO:"):], "<> "))
				reply("250 ok")
			case upper == "DATA":
				reply("354 go ahead")
				var b strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
					b.WriteString(l)
				}
				s.data = b.String()
				reply("250 queued")
			case upper == "QUIT":
				reply("221 bye")
				f.got <- s
				return
			default:
				reply("250 ok")
			}
		}
	}()
	return f
}

func TestSMTPNotify(t *testing.T) {
	srv := startFakeSMTP(t)
	host, port, _ := net.SplitHostPort(srv.addr)
	portNum, _ := strconv.Atoi(port)

	n := &smtpNotifier{cfg: smtpConfig{
		Host: host,
		Port: portNum,
		From: "SBA Academy <noreply@academysba.pro>",
		To:   []string{"manager@academysba.pro", "boss@academysba.pro"},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := n.Notify(ctx, sampleApp); err != nil {
		t.Fatal(err)
	}

	s := <-srv.got
	if s.from != "noreply@academysba.pro" {
		t.Errorf("MAIL FROM = %q", s.from)
	}
	if strings.Join(s.rcpt, ",") != "manager@academysba.pro,boss@academysba.pro" {
		t.Errorf("RCPT TO = %v", s.rcpt)
	}
	if !strings.Contains(s.data, "multipart/alternative") || !strings.Contains(s.data, "Reply-To: <anna@mail.ru>") {
		t.Errorf("письмо:\n%s", s.data)
	}
}
