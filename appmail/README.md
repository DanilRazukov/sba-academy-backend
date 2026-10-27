# Письмо о новой заявке (Go)

Шаблон уведомления менеджерам о заявке с формы «Оставьте заявку» на academysba.pro. Пакет `appmail` без внешних зависимостей, только стандартная библиотека Go.

## Использование

```go
msg, err := appmail.Render(appmail.Application{
	ID:         "A-1042",            // номер в CRM, можно пустым
	CourseID:   req.CourseID,        // поля формы — контракт src/shared/api/application.types.ts
	Name:       req.Name,
	Phone:      req.Phone,
	Email:      req.Email,
	ReceivedAt: time.Now(),
})
// msg.Subject, msg.HTML и msg.Text — в письмо multipart/alternative,
// msg.ReplyTo — в заголовок Reply-To, тогда ответ менеджера уйдёт клиенту.
```

Перед `Render` сервер проверяет поля и защиту от ботов: заявку с заполненной ловушкой `trap` или со временем заполнения `fillTimeMs` меньше 3000 мс отбрасывают и отвечают ей как успешной. Правила описаны в `ApplicationBotCheck` на фронте.

## Что внутри

- `templates/application.html.tmpl` — HTML-версия: таблицы и стили в атрибутах для почтовых клиентов, ширина 600, на телефоне кнопки встают в столбик. Цвета взяты из палитры сайта.
- `templates/application.txt.tmpl` — текстовая версия для клиентов без HTML.
- `appmail.go` — данные, каталог программ и форматирование: цена «30 000 ₽», время по Москве, ссылки `tel:` и `mailto:`.

Каталог программ `Courses` дублирует фронт (`src/entities/course/model/courses.data.ts`), менять их нужно вместе. Неизвестная программа выводится по своему id.

## Проверка

```bash
go test ./...
```

Чтобы посмотреть письмо в браузере, тест сохраняет HTML в папку из переменной окружения:

```bash
PREVIEW_DIR=/tmp go test ./...
```
