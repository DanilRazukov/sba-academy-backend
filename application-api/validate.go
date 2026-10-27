package main

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Проверка полей повторяет фронт: src/features/submit-application/model/validate.ts,
// тексты — formContent.validation. Менять вместе: форма показывает fieldErrors
// сервера как есть.

// applicationFields — поля заявки, контракт ApplicationFields (src/shared/api/application.types.ts).
type applicationFields struct {
	CourseID string `json:"courseId"`
	Name     string `json:"name"`
	Phone    string `json:"phone"`
	Email    string `json:"email"`
}

func (f applicationFields) trimmed() applicationFields {
	return applicationFields{
		CourseID: strings.TrimSpace(f.CourseID),
		Name:     strings.TrimSpace(f.Name),
		Phone:    strings.TrimSpace(f.Phone),
		Email:    strings.TrimSpace(f.Email),
	}
}

var (
	// id программы из каталога: ecosystem-basics, crypto-assets… Незнакомый id
	// пропускаем (каталог на фронте может обновиться раньше), лишь бы был похож на id.
	courseIDPattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	// буквы любого алфавита, пробел, дефис, апостроф и точка («Анна-Мария», «O'Neil»)
	namePattern = regexp.MustCompile(`^\p{L}[\p{L}\s\x{00A0}'’.-]*$`)
	// цифры, плюс, пробелы, скобки и дефисы — как люди пишут номера
	phonePattern = regexp.MustCompile(`^[+\d\s()-]+$`)
	// без пробелов, одна «@», точка в домене и зона от двух символов
	emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]{2,}$`)
)

// validateFields возвращает тексты ошибок по полям; пустая мапа — всё верно.
// Значения должны быть уже без пробелов по краям (trimmed).
func validateFields(f applicationFields) map[string]string {
	errs := map[string]string{}

	if f.CourseID == "" || !courseIDPattern.MatchString(f.CourseID) {
		errs["courseId"] = "Выберите программу"
	}

	switch n := utf8.RuneCountInString(f.Name); {
	case n == 0:
		errs["name"] = "Укажите, как к вам обращаться"
	case n < 2:
		errs["name"] = "Имя слишком короткое"
	case n > 100:
		errs["name"] = "Имя слишком длинное — до 100 символов"
	case !namePattern.MatchString(f.Name):
		errs["name"] = "Имя может содержать только буквы, пробел и дефис"
	}

	switch {
	case f.Phone == "":
		errs["phone"] = "Укажите телефон"
	case !phonePattern.MatchString(f.Phone):
		errs["phone"] = "В номере могут быть только цифры, +, пробелы, скобки и дефис"
	default:
		if d := countDigits(f.Phone); d < 10 || d > 15 {
			errs["phone"] = "Проверьте номер: в нём должно быть от 10 до 15 цифр"
		}
	}

	switch {
	case f.Email == "":
		errs["email"] = "Укажите электронную почту"
	case utf8.RuneCountInString(f.Email) > 254 || !emailPattern.MatchString(f.Email):
		errs["email"] = "Проверьте адрес: например, name@example.com"
	}

	return errs
}

func countDigits(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}
