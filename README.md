# SBA Academy — бэкенд лендинга

Серверная часть сайта [academysba.pro](https://academysba.pro/). Пока здесь только приём заявок с формы «Оставьте заявку»: заявка уходит менеджерам в Telegram и на почту. Код на Go, только стандартная библиотека.

| Модуль | Что делает |
|---|---|
| [`application-api/`](application-api/README.md) | HTTP-сервис `POST /applications`: проверка полей, защита от ботов, ограничение частоты, CORS, рассылка в каналы. Запускается в Docker |
| [`appmail/`](appmail/README.md) | Пакет, который собирает письмо о заявке (тема, HTML, текст). Его использует `application-api` |

## Связь с лендингом

Фронт — отдельный репозиторий `sba-academy` (Nuxt 4, статическая сборка). Контракт между ними:

- **Поля и ответы** описаны в `src/shared/api/application.types.ts` лендинга. Запрос и ответ сервиса совпадают с ними 1:1.
- **Отправку** делает `src/shared/api/submit-application.ts` лендинга. Адрес сервиса фронт берёт из `NUXT_PUBLIC_APPLICATION_API_URL` при сборке.
- **Проверки полей и тексты ошибок** продублированы: `application-api/validate.go` ↔ `src/features/submit-application/model/validate.ts` и `form-content.ts`.
- **Каталог программ** продублирован: `appmail.Courses` ↔ `src/entities/course/model/courses.data.ts`.

Пути `src/...` в комментариях к коду указывают на репозиторий лендинга.

**Меняя форму, правьте обе стороны.** Если в каталоге программ появилась новая, а здесь её нет, письмо и сообщение в Telegram покажут id программы вместо названия. Заявка при этом не потеряется.

## Разработка

```bash
# тесты обоих модулей (go.work связывает их для IDE и go test)
cd application-api && go test ./...
cd appmail && go test ./...

# сервис локально — настройки в application-api/.env (пример: .env.example)
docker build -f application-api/Dockerfile -t sba-application-api .
docker run --rm -p 8090:8080 --env-file application-api/.env -e PORT=8080 sba-application-api
```

## CI

GitHub Actions ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)):
- на каждый пуш и pull request запускаются `gofmt`, `go vet` и `go test -race` для обоих модулей;
- после зелёных тестов на `main` образ публикуется в GitHub Container Registry: `ghcr.io/danilrazukov/sba-academy-backend/application-api:latest` и тег с коротким sha коммита.

Хостинг сервиса и вопросы деплоя описаны в [`application-api/README.md`](application-api/README.md#деплой-на-что-обратить-внимание).
