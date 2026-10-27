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
	"sync"
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

func TestTelegramHTML(t *testing.T) {
	text := telegramHTML(sampleApp)
	for _, want := range []string{
		"🆕 <b>Новая заявка</b>  ·  A-261028-3f9a1c",
		"📚 <b>Криптовалюта и виртуальные активы</b>\nФлагманский курс · " + appmail.FormatPrice(30000),
		"👤 Анна Комарова",
		"📞 <code>+7 (900) 123-45-67</code>",
		"✉️ anna@mail.ru",
		"🕒 28 октября 2026, 14:05 МСК",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("нет %q в:\n%s", want, text)
		}
	}

	app := sampleApp
	app.CourseID = "new-course"
	if !strings.Contains(telegramHTML(app), "📚 <b>new-course</b>") {
		t.Error("неизвестная программа должна выводиться по id")
	}
}

func TestTelegramHTMLEscapesInput(t *testing.T) {
	app := sampleApp
	app.Name = `<b>Взлом</b> & "кавычки"`
	text := telegramHTML(app)
	if strings.Contains(text, "<b>Взлом</b>") {
		t.Errorf("ввод пользователя не экранирован:\n%s", text)
	}
	if !strings.Contains(text, "&lt;b&gt;Взлом&lt;/b&gt; &amp; &#34;кавычки&#34;") {
		t.Errorf("неожиданное экранирование:\n%s", text)
	}
}

func TestInternationalDigits(t *testing.T) {
	for in, want := range map[string]string{
		"+7 (900) 123-45-67": "79001234567",
		"8 900 123-45-67":    "79001234567",
		"(900) 123-45-67":    "79001234567",
		"+375 29 123-45-67":  "375291234567",
		"+44 20 7946 0958":   "442079460958",
		"123-45-67":          "",
		"+1 234 567 890":     "", // 10 цифр с «+» — код страны неизвестен, не гадаем
	} {
		if got := internationalDigits(in); got != want {
			t.Errorf("internationalDigits(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContactButtons(t *testing.T) {
	kb := contactButtons("8 (900) 123-45-67")
	if kb == nil || len(kb.InlineKeyboard) != 1 || len(kb.InlineKeyboard[0]) != 2 {
		t.Fatalf("кнопки: %+v", kb)
	}
	if kb.InlineKeyboard[0][0].URL != "https://wa.me/79001234567" || kb.InlineKeyboard[0][1].URL != "https://t.me/+79001234567" {
		t.Errorf("ссылки: %+v", kb.InlineKeyboard[0])
	}
	if contactButtons("123") != nil {
		t.Error("для неразобранного номера кнопок быть не должно")
	}
}

// fakeTelegram — поддельный Bot API: запоминает запросы и отвечает тем,
// что вернёт reply.
func fakeTelegram(t *testing.T, reply func(msg telegramMessage) (status int, body string)) (*httptest.Server, *[]telegramMessage, *string) {
	t.Helper()
	var (
		mu   sync.Mutex
		got  []telegramMessage
		path string
	)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg telegramMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		mu.Lock()
		got, path = append(got, msg), r.URL.Path
		mu.Unlock()
		status, body := reply(msg)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(api.Close)
	return api, &got, &path
}

func TestTelegramNotify(t *testing.T) {
	api, got, path := fakeTelegram(t, func(msg telegramMessage) (int, string) {
		if msg.ChatID == "bad" {
			return http.StatusBadRequest, `{"ok":false,"description":"Bad Request: chat not found"}`
		}
		return http.StatusOK, `{"ok":true,"result":{}}`
	})

	tg := newTelegramNotifier(telegramConfig{BotToken: "123:SECRET", ChatID: "-100500", APIBase: api.URL})
	if err := tg.Notify(context.Background(), sampleApp); err != nil {
		t.Fatal(err)
	}
	if *path != "/bot123:SECRET/sendMessage" {
		t.Errorf("path = %q", *path)
	}
	msg := (*got)[0]
	if msg.ChatID != "-100500" || msg.ParseMode != "HTML" || !strings.Contains(msg.Text, "Анна Комарова") {
		t.Errorf("запрос: %+v", msg)
	}
	if msg.ReplyMarkup == nil || msg.ReplyMarkup.InlineKeyboard[0][0].URL != "https://wa.me/79001234567" {
		t.Errorf("кнопки: %+v", msg.ReplyMarkup)
	}

	tg.cfg.ChatID = "bad"
	err := tg.Notify(context.Background(), sampleApp)
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("ошибка API: %v", err)
	}
}

func TestTelegramFallsBackToPlainText(t *testing.T) {
	// Telegram отвергает оформление — заявка всё равно уходит простым текстом
	api, got, _ := fakeTelegram(t, func(msg telegramMessage) (int, string) {
		if msg.ParseMode == "HTML" {
			return http.StatusBadRequest, `{"ok":false,"description":"Bad Request: can't parse entities"}`
		}
		return http.StatusOK, `{"ok":true,"result":{}}`
	})

	tg := newTelegramNotifier(telegramConfig{BotToken: "1:x", ChatID: "1", APIBase: api.URL})
	if err := tg.Notify(context.Background(), sampleApp); err != nil {
		t.Fatalf("запасной вариант не сработал: %v", err)
	}
	if len(*got) != 2 {
		t.Fatalf("запросов %d, want 2", len(*got))
	}
	plain := (*got)[1]
	if plain.ParseMode != "" || plain.ReplyMarkup != nil || !strings.Contains(plain.Text, "Имя: Анна Комарова") {
		t.Errorf("запасное сообщение: %+v", plain)
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

func TestResendNotify(t *testing.T) {
	var (
		got  resendEmail
		path string
		auth string
	)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		if auth != "Bearer re_good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"statusCode":401,"name":"validation_error","message":"API key is invalid"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"49a3999c-0ce1-4ea6-ab68-afcd6dc2e794"}`)
	}))
	defer api.Close()

	r := newResendNotifier(resendConfig{
		APIKey:  "re_good",
		APIBase: api.URL,
		From:    "SBA Academy <onboarding@resend.dev>",
		To:      []string{"manager@academysba.pro", "boss@academysba.pro"},
	})
	if err := r.Notify(context.Background(), sampleApp); err != nil {
		t.Fatal(err)
	}

	if path != "/emails" || auth != "Bearer re_good" {
		t.Errorf("запрос: path=%q auth=%q", path, auth)
	}
	if got.From != "SBA Academy <onboarding@resend.dev>" {
		t.Errorf("from = %q", got.From)
	}
	if len(got.To) != 2 || got.To[1] != "boss@academysba.pro" {
		t.Errorf("to = %v", got.To)
	}
	if got.ReplyTo != "anna@mail.ru" {
		t.Errorf("reply_to = %q", got.ReplyTo)
	}
	if !strings.Contains(got.Subject, "Анна Комарова") || !strings.Contains(got.HTML, "Анна Комарова") || !strings.Contains(got.Text, "Анна Комарова") {
		t.Error("в письме нет данных заявки")
	}

	r.cfg.APIKey = "re_bad"
	err := r.Notify(context.Background(), sampleApp)
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "API key is invalid") {
		t.Errorf("ошибка API: %v", err)
	}
	if strings.Contains(err.Error(), "re_bad") {
		t.Errorf("ключ попал в ошибку: %v", err)
	}
}
