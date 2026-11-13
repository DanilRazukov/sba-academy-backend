// Package appmail собирает письмо менеджерам о новой заявке с сайта
// academysba.pro: тема, HTML и текстовая версия (для multipart/alternative).
//
// Данные приходят из формы «Оставьте заявку» — контракт на фронте:
// src/shared/api/application.types.ts (ApplicationFields). Сервер сначала
// проверяет поля и защиту от ботов (ApplicationBotCheck), потом вызывает Render.
package appmail

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	"net/url"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"
)

// Application — заявка в том виде, как её прислал фронт, плюс то, что
// добавляет сервер (номер и время получения).
type Application struct {
	// ID — номер заявки в CRM или базе; пустой — строка в письме не выводится.
	ID string
	// CourseID — id программы: ecosystem-basics, crypto-assets, tokenomics, partner-programs.
	CourseID string
	Name     string
	// Phone — как ввёл пользователь, например «+7 900 123-45-67».
	Phone string
	Email string
	// ReceivedAt — когда сервер принял заявку; в письме — по Москве.
	ReceivedAt time.Time
}

// Course — программа обучения. Каталог дублирует фронт
// (src/entities/course/model/courses.data.ts) — менять вместе.
type Course struct {
	Title string
	Kind  string
	Price int // рубли
}

// Courses — каталог программ по id.
var Courses = map[string]Course{
	"ecosystem-basics": {Title: "Основы экосистемы", Kind: "Вводный курс", Price: 15000},
	"crypto-assets":    {Title: "Криптовалюта и виртуальные активы", Kind: "Основной курс", Price: 30000},
	"tokenomics":       {Title: "Токеномика без иллюзий", Kind: "Специализация", Price: 15000},
	"partner-programs": {Title: "Партнёрские программы", Kind: "Для бизнеса", Price: 20000},
}

// Message — готовое письмо. Отправитель кладёт HTML и Text в
// multipart/alternative; Reply-To стоит выставить на ReplyTo — тогда ответ
// менеджера уйдёт клиенту.
type Message struct {
	Subject string
	HTML    string
	Text    string
	ReplyTo string
}

//go:embed templates/*.tmpl
var files embed.FS

var (
	htmlTmpl = htmltemplate.Must(htmltemplate.ParseFS(files, "templates/application.html.tmpl"))
	textTmpl = texttemplate.Must(texttemplate.ParseFS(files, "templates/application.txt.tmpl"))
)

// Москва без перехода на летнее время с 2014 года — фиксированный UTC+3,
// без зависимости от tzdata на сервере.
var moscow = time.FixedZone("МСК", 3*60*60)

var months = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня",
	"июля", "августа", "сентября", "октября", "ноября", "декабря"}

// view — данные шаблона: всё уже отформатировано, шаблоны только выводят.
type view struct {
	ID          string
	Name        string
	Phone       string
	Email       string
	CourseTitle string
	CourseMeta  string // «Основной курс · 30 000 ₽»
	ReceivedAt  string // «28 октября 2026, 14:05 МСК»
	Preheader   string // строка-превью в списке писем
	// ссылки собраны и экранированы в Go: html/template сам не пропускает
	// схему tel:, поэтому значения помечены как безопасные здесь
	PhoneHref htmltemplate.URL
	EmailHref htmltemplate.URL
}

// Render собирает письмо по заявке.
func Render(app Application) (Message, error) {
	v := newView(app)

	var html, text bytes.Buffer
	if err := htmlTmpl.Execute(&html, v); err != nil {
		return Message{}, fmt.Errorf("appmail: html: %w", err)
	}
	if err := textTmpl.Execute(&text, v); err != nil {
		return Message{}, fmt.Errorf("appmail: text: %w", err)
	}

	return Message{
		Subject: fmt.Sprintf("Новая заявка: %s — %s", v.CourseTitle, v.Name),
		HTML:    html.String(),
		Text:    text.String(),
		ReplyTo: v.Email,
	}, nil
}

func newView(app Application) view {
	name := strings.TrimSpace(app.Name)
	email := strings.TrimSpace(app.Email)
	phone := strings.TrimSpace(app.Phone)

	title, meta := app.CourseID, ""
	if c, ok := Courses[app.CourseID]; ok {
		title, meta = c.Title, c.Kind+" · "+FormatPrice(c.Price)
	}

	return view{
		ID:          strings.TrimSpace(app.ID),
		Name:        name,
		Phone:       phone,
		Email:       email,
		CourseTitle: title,
		CourseMeta:  meta,
		ReceivedAt:  FormatTime(app.ReceivedAt),
		Preheader:   fmt.Sprintf("%s, %s — свяжитесь в рабочее время", name, phone),
		PhoneHref:   htmltemplate.URL("tel:" + phoneDigits(phone)),
		EmailHref: htmltemplate.URL("mailto:" + url.PathEscape(email) +
			"?subject=" + url.PathEscape("Заявка на программу «"+title+"»")),
	}
}

// phoneDigits оставляет цифры и ведущий плюс: «+7 (900) 123-45-67» → «+79001234567».
func phoneDigits(phone string) string {
	var b strings.Builder
	for i, r := range phone {
		if r >= '0' && r <= '9' || r == '+' && i == 0 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FormatPrice — «30 000 ₽» с неразрывными пробелами, как на сайте.
// Экспортирован для других каналов уведомлений (Telegram).
func FormatPrice(rub int) string {
	s := strconv.Itoa(rub)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	return b.String() + " ₽"
}

// FormatTime — «28 октября 2026, 14:05 МСК»; нулевое время — текущее.
func FormatTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	t = t.In(moscow)
	return fmt.Sprintf("%d %s %d, %02d:%02d МСК", t.Day(), months[t.Month()-1], t.Year(), t.Hour(), t.Minute())
}
