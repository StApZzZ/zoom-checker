# Zoom Checker в Docker

Образ — один статический бинарник на `distroless` (без оболочки, работает не от root), около 30 МБ.
Сервису хватает 30–60 МБ памяти. Данные (SQLite, сертификаты, резервные копии) лежат в томе `/data`.

## Быстрый старт (свой HTTPS от Let's Encrypt)

```bash
git clone https://github.com/StApZzZ/zoom-checker.git
cd zoom-checker/deploy
cp .env.example .env && chmod 600 .env       # в файле будут пароли — читать его может только владелец
nano .env
#   PUBLIC_URL=https://attendance.example.com   (доменное имя: IP-адрес Zoom не примет,
#                                               https://203.0.113.10 годится только для проверки сервиса)
#   ZOOM_WEBHOOK_SECRET_TOKEN=...              (Secret Token из приложения Zoom)
#   TLS_MODE=auto
#   TIMEZONE=Asia/Yekaterinburg                (если сервис не по Москве: «сегодня» и время в сообщениях)
docker compose run --rm zoom-checker check-config   # проверить настройки до запуска
docker compose up -d --build
docker compose logs -f zoom-checker
docker compose ps        # через полминуты в колонке STATUS должно быть «healthy»
```

Без файла `.env` Compose не запустится («env file … .env not found») — сначала скопируйте `.env.example`.
Порты 80 и 443 должны быть открыты из интернета: через них Let's Encrypt проверяет сервер. В облаке
откройте их ещё и в группе безопасности (файрволе) провайдера — см.
[admin-guide, раздел 2](../docs/admin-guide.md#откройте-порты-80-и-443-у-облачного-провайдера).
Адрес для Zoom (Event notification endpoint URL): `<PUBLIC_URL>/api/webhooks/zoom`.
Часы сервера должны быть синхронизированы (`timedatectl` → `System clock synchronized: yes`; контейнер берёт
время у сервера): если часы спешат больше чем на 2 часа, события Zoom не принимаются.

## Первый вход

Проще всего заранее задать пароль в `.env`: `ADMIN_PASSWORD=...` (логин — `ADMIN_USERNAME`, по умолчанию `admin`).
Слишком простой пароль (например, короче 8 символов) сервис попросит сменить при первом входе. После первого
входа удалите строку `ADMIN_PASSWORD` из `.env`: она нужна, только пока в базе нет пользователей.
Если пароль не задан, сервис сам придумает его при первом запуске и запишет в том. В образе нет оболочки, поэтому файл сначала копируется наружу:

```bash
docker compose cp zoom-checker:/data/initial-admin-password.txt . && cat initial-admin-password.txt && rm initial-admin-password.txt
```

Или выдать новый пароль (он будет показан один раз):

```bash
docker compose exec zoom-checker /zoom-checker user set-password admin --generate
```

## За своим прокси (nginx, Caddy, балансировщик)

В `.env`: `TLS_MODE=off`, `LISTEN_ADDR=:8080`, `TRUSTED_PROXIES=<адрес прокси>`.
Если прокси работает на этом же сервере, его запросы приходят в контейнер из сети Docker,
а не с `127.0.0.1`, поэтому укажите `TRUSTED_PROXIES=172.16.0.0/12`.
В `docker-compose.yml` замените порты `80:80` и `443:443` на `127.0.0.1:8080:8080`.
Прокси должен отдавать SSE без буферизации (`proxy_buffering off;` для `/api/`).

## Попробовать на своём компьютере (без HTTPS)

В `.env`: `PUBLIC_URL=http://localhost:8080`, `TLS_MODE=off`, `LISTEN_ADDR=:8080`, `ADMIN_PASSWORD=...`,
порты в `docker-compose.yml` — `127.0.0.1:8080:8080`. Откройте http://localhost:8080.
Настоящие события Zoom так не придут (Zoom шлёт их только на https-адрес), но их можно сымитировать — см. `simulate` ниже.

## Файлы сертификатов (LDAP_CA_FILE, OIDC_CA_FILE, свой TLS-сертификат)

Контейнер не видит папки сервера: путь вида `/etc/zoom-checker/corp-ca.pem` внутри него не существует,
и сервис будет перезапускаться по кругу. Положите файлы в папку `deploy/certs`, отдайте их пользователю
контейнера (`sudo chown -R 65532:65532 certs`, ключ — `sudo chmod 600 certs/tls.key`), раскомментируйте
в `docker-compose.yml` строку `- ./certs:/certs:ro` и укажите в `.env` пути внутри контейнера:
`LDAP_CA_FILE=/certs/corp-ca.pem`, `TLS_CERT_FILE=/certs/tls.crt` и т. д. Проверка:
`docker compose run --rm zoom-checker check-config`. Подробно — в
[admin-guide, раздел 4](../docs/admin-guide.md#4-установка-в-docker).

## PostgreSQL вместо SQLite

В `.env` (пароль — только латинские буквы и цифры, например `openssl rand -hex 24`: символы `@ / %`
и пробел ломают строку `DATABASE_URL`):

```
COMPOSE_PROFILES=postgres
POSTGRES_PASSWORD=<надёжный пароль>
DATABASE_URL=postgres://zoom_checker:<тот же пароль>@postgres:5432/zoom_checker?sslmode=disable
```

и запуск как обычно: `docker compose up -d --build`. Строка `COMPOSE_PROFILES=postgres` включает контейнер
PostgreSQL во все команды `docker compose` (`up`, `down`, `exec`), отдельно `--profile` указывать не нужно.
Резервная копия базы:

```bash
docker compose exec -T postgres pg_dump -U zoom_checker -Fc zoom_checker > zoom-checker.dump
```

## Полезные команды

```bash
docker compose run --rm zoom-checker check-config                    # проверить настройки (работает, даже если контейнер не запускается)
docker compose exec zoom-checker /zoom-checker user list
docker compose exec zoom-checker /zoom-checker backup /data/manual.db  # копия SQLite в том
docker compose exec zoom-checker /zoom-checker check-ldap --user ivanov
# демонстрация: «урок» за 3 минуты с подписанными событиями Zoom
# (сначала создайте занятие с конференцией 857 4606 5432 на текущее время)
docker compose exec zoom-checker /zoom-checker simulate --meeting 85746065432
```

`simulate` отправляет события на `PUBLIC_URL`. Если этот адрес недоступен изнутри контейнера
(например, за прокси или проба на своём компьютере), укажите адрес сервиса в контейнере:

* `TLS_MODE=off`: `--url http://127.0.0.1:8080/api/webhooks/zoom` (порт из `LISTEN_ADDR`);
* `TLS_MODE=auto`: `--url https://127.0.0.1/api/webhooks/zoom --insecure` (сертификат выдан на доменное имя,
  а не на 127.0.0.1, поэтому его проверку нужно отключить);
* `TLS_MODE=file`: `--url https://127.0.0.1:<порт из LISTEN_ADDR>/api/webhooks/zoom --insecure`.

Ежедневные резервные копии SQLite — в `/data/backups` (хранится `BACKUP_KEEP` последних).
Если вместо тома `zc-data` подключаете папку сервера (`./data:/data`), отдайте её пользователю
контейнера: `sudo chown 65532:65532 ./data` — иначе сервис не сможет записать базу.
Обновление: сначала обновите исходный код (`git pull` или распакуйте новый архив поверх старого — файл
`deploy/.env` останется), затем `docker compose up -d --build` (данные в томе сохраняются, миграции
применяются сами). Без обновления кода `--build` просто пересоберёт ту же версию.
Удалить всё вместе с данными: `docker compose down -v` (без `-v` тома с базой остаются).
