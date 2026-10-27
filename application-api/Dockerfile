# Сборка из корня репозитория (нужен соседний модуль appmail):
#   docker build -f application-api/Dockerfile -t sba-application-api .
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY appmail/ appmail/
COPY application-api/ application-api/
WORKDIR /src/application-api
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/application-api .

# distroless/static: только бинарник и корневые сертификаты (нужны для TLS
# к Telegram и SMTP), без оболочки; запуск не от root
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/application-api /application-api
EXPOSE 8080
ENTRYPOINT ["/application-api"]
