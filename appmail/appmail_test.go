package appmail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var sample = Application{
	ID:         "A-1042",
	CourseID:   "crypto-assets",
	Name:       "Анна Комарова",
	Phone:      "+7 (900) 123-45-67",
	Email:      "anna@mail.ru",
	ReceivedAt: time.Date(2026, time.October, 28, 11, 5, 0, 0, time.UTC),
}

func TestRender(t *testing.T) {
	msg, err := Render(sample)
	if err != nil {
		t.Fatal(err)
	}

	if want := "Новая заявка: Криптовалюта и виртуальные активы — Анна Комарова"; msg.Subject != want {
		t.Errorf("subject = %q, want %q", msg.Subject, want)
	}
	if msg.ReplyTo != "anna@mail.ru" {
		t.Errorf("reply-to = %q", msg.ReplyTo)
	}
	for _, want := range []string{
		`href="tel:&#43;79001234567"`, // «+» в атрибуте html/template пишет сущностью
		`href="mailto:anna@mail.ru?subject=`,
		"28 октября 2026, 14:05 МСК",
		"Флагманский курс · 30 000 ₽",
		"№&nbsp;A-1042",
	} {
		if !strings.Contains(msg.HTML, want) {
			t.Errorf("html: нет %q", want)
		}
	}
	for _, want := range []string{"Программа: Криптовалюта и виртуальные активы", "Телефон:   +7 (900) 123-45-67", "№ A-1042"} {
		if !strings.Contains(msg.Text, want) {
			t.Errorf("text: нет %q", want)
		}
	}

	// PREVIEW_DIR=… go test — сохранить HTML для просмотра в браузере
	if dir := os.Getenv("PREVIEW_DIR"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "application-email.html"), []byte(msg.HTML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderEscapesUserInput(t *testing.T) {
	app := sample
	app.Name = `<script>alert(1)</script>`
	msg, err := Render(app)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg.HTML, "<script>alert") {
		t.Error("имя клиента попало в HTML без экранирования")
	}
}

func TestRenderUnknownCourseAndNoID(t *testing.T) {
	app := sample
	app.ID = ""
	app.CourseID = "new-course"
	msg, err := Render(app)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.Subject, "new-course") {
		t.Errorf("неизвестная программа должна выводиться по id: %q", msg.Subject)
	}
	if strings.Contains(msg.HTML, "№") || strings.Contains(msg.Text, "№") {
		t.Error("без ID номер заявки выводиться не должен")
	}
}

func TestFormatPrice(t *testing.T) {
	cases := map[int]string{900: "900 ₽", 15000: "15 000 ₽", 1250000: "1 250 000 ₽"}
	for in, want := range cases {
		if got := FormatPrice(in); got != want {
			t.Errorf("FormatPrice(%d) = %q, want %q", in, got, want)
		}
	}
}
