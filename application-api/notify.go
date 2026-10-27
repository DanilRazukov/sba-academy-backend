package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DanilRazukov/sba-academy-backend/appmail"
)

// notifier — канал доставки заявки менеджерам.
type notifier interface {
	Name() string
	Notify(ctx context.Context, app appmail.Application) error
}

// moscow — время в уведомлениях; фиксированный UTC+3 без зависимости от tzdata.
var moscow = time.FixedZone("МСК", 3*60*60)

// ---------- Telegram ----------

type telegramNotifier struct {
	cfg    telegramConfig
	client *http.Client
}

func newTelegramNotifier(cfg telegramConfig) *telegramNotifier {
	return &telegramNotifier{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}
}

func (t *telegramNotifier) Name() string { return "telegram" }

// telegramMessage — тело sendMessage.
type telegramMessage struct {
	ChatID                string          `json:"chat_id"`
	Text                  string          `json:"text"`
	ParseMode             string          `json:"parse_mode,omitempty"`
	DisableWebPagePreview bool            `json:"disable_web_page_preview"`
	ReplyMarkup           *inlineKeyboard `json:"reply_markup,omitempty"`
}

type inlineKeyboard struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

type inlineButton struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

// telegramAPIError — Telegram ответил ok:false.
type telegramAPIError struct {
	Status      int
	Description string
}

func (e *telegramAPIError) Error() string {
	return fmt.Sprintf("telegram: HTTP %d: %s", e.Status, e.Description)
}

func (t *telegramNotifier) Notify(ctx context.Context, app appmail.Application) error {
	err := t.send(ctx, telegramMessage{
		ChatID:                t.cfg.ChatID,
		Text:                  telegramHTML(app),
		ParseMode:             "HTML",
		DisableWebPagePreview: true,
		ReplyMarkup:           contactButtons(app.Phone),
	})

	// Оформление Telegram может отвергнуть (400: «can't parse entities»,
	// «wrong HTTP URL» у кнопки). Доставить заявку важнее, чем украсить, —
	// повторяем обычным текстом без кнопок.
	var apiErr *telegramAPIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest {
		return t.send(ctx, telegramMessage{
			ChatID:                t.cfg.ChatID,
			Text:                  telegramPlainText(app),
			DisableWebPagePreview: true,
		})
	}
	return err
}

func (t *telegramNotifier) send(ctx context.Context, msg telegramMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	endpoint := strings.TrimRight(t.cfg.APIBase, "/") + "/bot" + t.cfg.BotToken + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("telegram: не удалось собрать запрос")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		// url.Error содержит адрес с токеном бота — в лог его не пускаем
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("telegram: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return fmt.Errorf("telegram: HTTP %d, ответ не разобран: %w", resp.StatusCode, err)
	}
	if !result.OK {
		return &telegramAPIError{Status: resp.StatusCode, Description: result.Description}
	}
	return nil
}

// telegramHTML — карточка заявки (parse_mode HTML). Всё, что ввёл
// пользователь, экранируется. Телефон — моноширинный: копируется в один тап.
func telegramHTML(app appmail.Application) string {
	e := html.EscapeString
	var b strings.Builder

	b.WriteString("🆕 <b>Новая заявка</b>")
	if app.ID != "" {
		b.WriteString("  ·  " + e(app.ID))
	}
	b.WriteString("\n\n")
	if c, ok := appmail.Courses[app.CourseID]; ok {
		b.WriteString("📚 <b>" + e(c.Title) + "</b>\n" + e(c.Kind) + " · " + appmail.FormatPrice(c.Price))
	} else {
		b.WriteString("📚 <b>" + e(app.CourseID) + "</b>")
	}
	b.WriteString("\n\n👤 " + e(app.Name))
	b.WriteString("\n📞 <code>" + e(app.Phone) + "</code>")
	b.WriteString("\n✉️ " + e(app.Email))
	b.WriteString("\n\n🕒 " + appmail.FormatTime(app.ReceivedAt))
	return b.String()
}

// telegramPlainText — запасной вид без разметки, если HTML отвергнут.
func telegramPlainText(app appmail.Application) string {
	course := app.CourseID
	if c, ok := appmail.Courses[app.CourseID]; ok {
		course = c.Title + " (" + c.Kind + ")"
	}

	var b strings.Builder
	b.WriteString("Новая заявка с сайта")
	if app.ID != "" {
		b.WriteString(" · № " + app.ID)
	}
	b.WriteString("\n\nПрограмма: " + course)
	b.WriteString("\nИмя: " + app.Name)
	b.WriteString("\nТелефон: " + app.Phone)
	b.WriteString("\nПочта: " + app.Email)
	b.WriteString("\nПолучена: " + appmail.FormatTime(app.ReceivedAt))
	return b.String()
}

// contactButtons — кнопки «написать клиенту» в WhatsApp и Telegram.
// «Позвонить» сделать нельзя: Telegram не принимает в кнопках ссылки tel:.
// Номер, который не получается привести к международному виду, — без кнопок.
func contactButtons(phone string) *inlineKeyboard {
	digits := internationalDigits(phone)
	if digits == "" {
		return nil
	}
	return &inlineKeyboard{InlineKeyboard: [][]inlineButton{{
		{Text: "💬 WhatsApp", URL: "https://wa.me/" + digits},
		{Text: "✈️ Telegram", URL: "https://t.me/+" + digits},
	}}}
}

// internationalDigits — номер цифрами в международном формате без «+»:
// «+7 (900) 123-45-67» → «79001234567». Российские номера без кода страны
// дописываем: «8 900 …» и «900 …» → «7900…». Пустая строка — номер не разобран.
func internationalDigits(phone string) string {
	var b strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()

	if !strings.HasPrefix(strings.TrimSpace(phone), "+") {
		switch {
		case len(digits) == 11 && digits[0] == '8':
			digits = "7" + digits[1:]
		case len(digits) == 10:
			digits = "7" + digits
		}
	}
	if len(digits) < 11 || len(digits) > 15 {
		return ""
	}
	return digits
}

// ---------- Resend (почта по HTTP API) ----------

type resendNotifier struct {
	cfg    resendConfig
	client *http.Client
}

func newResendNotifier(cfg resendConfig) *resendNotifier {
	return &resendNotifier{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}
}

func (r *resendNotifier) Name() string { return "resend" }

// resendEmail — тело POST /emails.
type resendEmail struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	ReplyTo string   `json:"reply_to,omitempty"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
	Text    string   `json:"text"`
}

func (r *resendNotifier) Notify(ctx context.Context, app appmail.Application) error {
	msg, err := appmail.Render(app)
	if err != nil {
		return fmt.Errorf("resend: %w", err)
	}

	email := resendEmail{
		From:    r.cfg.From,
		To:      r.cfg.To,
		Subject: msg.Subject,
		HTML:    msg.HTML,
		Text:    msg.Text,
	}
	// Reply-To — клиент: ответ менеджера уйдёт ему
	if replyTo, err := mail.ParseAddress(msg.ReplyTo); err == nil {
		email.ReplyTo = replyTo.Address
	}

	body, err := json.Marshal(email)
	if err != nil {
		return fmt.Errorf("resend: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.cfg.APIBase, "/")+"/emails", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("resend: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.cfg.APIKey)

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("resend: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 == 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	// ошибка API: {"statusCode":403,"name":"validation_error","message":"You can only send testing emails to your own email address…"}
	var apiErr struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&apiErr)
	return fmt.Errorf("resend: HTTP %d: %s: %s", resp.StatusCode, apiErr.Name, apiErr.Message)
}

// ---------- SMTP ----------

type smtpNotifier struct {
	cfg smtpConfig
}

func (s *smtpNotifier) Name() string { return "email" }

func (s *smtpNotifier) Notify(ctx context.Context, app appmail.Application) error {
	msg, err := appmail.Render(app)
	if err != nil {
		return fmt.Errorf("email: %w", err)
	}

	from, err := mail.ParseAddress(s.cfg.From)
	if err != nil {
		return fmt.Errorf("email: отправитель: %w", err)
	}
	var to []*mail.Address
	for _, raw := range s.cfg.To {
		a, err := mail.ParseAddress(raw)
		if err != nil {
			return fmt.Errorf("email: SMTP_TO: %w", err)
		}
		to = append(to, a)
	}

	data, err := buildMessage(from, to, msg, app.ReceivedAt)
	if err != nil {
		return fmt.Errorf("email: %w", err)
	}
	if err := s.send(ctx, from.Address, to, data); err != nil {
		return fmt.Errorf("email: %w", err)
	}
	return nil
}

// send — один SMTP-диалог. Порт 465 — TLS сразу; иначе STARTTLS, если сервер
// его предлагает (без TLS авторизация запрещена: net/smtp не отдаст пароль
// открытым текстом никому, кроме localhost).
func (s *smtpNotifier) send(ctx context.Context, from string, to []*mail.Address, data []byte) error {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	tlsConfig := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if s.cfg.Port == 465 {
		conn = tls.Client(conn, tlsConfig)
	}

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	if s.cfg.Port != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsConfig); err != nil {
				return err
			}
		}
	}
	if s.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, a := range to {
		if err := c.Rcpt(a.Address); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// buildMessage собирает письмо multipart/alternative: текст и HTML в
// quoted-printable, тема — encoded-word UTF-8.
func buildMessage(from *mail.Address, to []*mail.Address, msg appmail.Message, date time.Time) ([]byte, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, part := range []struct{ contentType, content string }{
		{"text/plain; charset=utf-8", msg.Text},
		{"text/html; charset=utf-8", msg.HTML},
	} {
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.contentType},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(pw)
		if _, err := io.WriteString(qp, part.content); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	recipients := make([]string, len(to))
	for i, a := range to {
		recipients[i] = a.String()
	}
	if date.IsZero() {
		date = time.Now()
	}

	var head bytes.Buffer
	writeHeader := func(k, v string) { fmt.Fprintf(&head, "%s: %s\r\n", k, v) }
	writeHeader("From", from.String())
	writeHeader("To", strings.Join(recipients, ", "))
	// Reply-To — клиент: ответ менеджера уйдёт ему. Адрес уже проверен
	// валидацией, ParseAddress — страховка от инъекции заголовков.
	if replyTo, err := mail.ParseAddress(msg.ReplyTo); err == nil {
		writeHeader("Reply-To", replyTo.String())
	}
	writeHeader("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	writeHeader("Date", date.Format(time.RFC1123Z))
	writeHeader("Message-ID", "<"+randomHex(12)+"@"+domainOf(from.Address)+">")
	writeHeader("MIME-Version", "1.0")
	writeHeader("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
	head.WriteString("\r\n")

	return append(head.Bytes(), body.Bytes()...), nil
}

func domainOf(address string) string {
	if i := strings.LastIndexByte(address, '@'); i >= 0 {
		return address[i+1:]
	}
	return "localhost"
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read не возвращает ошибку с Go 1.24
	return hex.EncodeToString(b)
}
