package main

import (
	"strings"
	"testing"
)

func validFields() applicationFields {
	return applicationFields{
		CourseID: "crypto-assets",
		Name:     "Анна-Мария O'Neil",
		Phone:    "+7 (900) 123-45-67",
		Email:    "anna@mail.ru",
	}
}

func TestValidateFieldsAcceptsValid(t *testing.T) {
	if errs := validateFields(validFields()); len(errs) != 0 {
		t.Fatalf("ошибки на верной заявке: %v", errs)
	}
}

func TestValidateFields(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*applicationFields)
		field  string
		want   string
	}{
		{"нет программы", func(f *applicationFields) { f.CourseID = "" }, "courseId", "Выберите программу"},
		{"программа не похожа на id", func(f *applicationFields) { f.CourseID = "<script>" }, "courseId", "Выберите программу"},
		{"нет имени", func(f *applicationFields) { f.Name = "" }, "name", "Укажите, как к вам обращаться"},
		{"короткое имя", func(f *applicationFields) { f.Name = "А" }, "name", "Имя слишком короткое"},
		{"длинное имя", func(f *applicationFields) { f.Name = strings.Repeat("я", 101) }, "name", "Имя слишком длинное — до 100 символов"},
		{"цифры в имени", func(f *applicationFields) { f.Name = "Анна2" }, "name", "Имя может содержать только буквы, пробел и дефис"},
		{"имя с дефиса", func(f *applicationFields) { f.Name = "-Анна" }, "name", "Имя может содержать только буквы, пробел и дефис"},
		{"нет телефона", func(f *applicationFields) { f.Phone = "" }, "phone", "Укажите телефон"},
		{"буквы в телефоне", func(f *applicationFields) { f.Phone = "+7 900 abc" }, "phone", "В номере могут быть только цифры, +, пробелы, скобки и дефис"},
		{"мало цифр", func(f *applicationFields) { f.Phone = "123-45-67" }, "phone", "Проверьте номер: в нём должно быть от 10 до 15 цифр"},
		{"много цифр", func(f *applicationFields) { f.Phone = "+1234567890123456" }, "phone", "Проверьте номер: в нём должно быть от 10 до 15 цифр"},
		{"нет почты", func(f *applicationFields) { f.Email = "" }, "email", "Укажите электронную почту"},
		{"почта без зоны", func(f *applicationFields) { f.Email = "anna@mail" }, "email", "Проверьте адрес: например, name@example.com"},
		{"две @", func(f *applicationFields) { f.Email = "a@b@mail.ru" }, "email", "Проверьте адрес: например, name@example.com"},
		{"перевод строки в почте", func(f *applicationFields) { f.Email = "a@mail.ru\r\nBcc: x@y.z" }, "email", "Проверьте адрес: например, name@example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := validFields()
			tt.modify(&f)
			errs := validateFields(f)
			if errs[tt.field] != tt.want {
				t.Errorf("%s = %q, want %q", tt.field, errs[tt.field], tt.want)
			}
			if len(errs) != 1 {
				t.Errorf("ожидалась одна ошибка, получено %v", errs)
			}
		})
	}
}

func TestTrimmed(t *testing.T) {
	f := applicationFields{CourseID: " tokenomics ", Name: "  Анна ", Phone: " +79001234567\t", Email: " a@b.ru "}.trimmed()
	if f.CourseID != "tokenomics" || f.Name != "Анна" || f.Phone != "+79001234567" || f.Email != "a@b.ru" {
		t.Errorf("trimmed() = %+v", f)
	}
}
