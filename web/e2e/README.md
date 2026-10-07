# Browser end-to-end tests (Playwright)

The suite drives the **real binary** (`bin/zoom-checker`, built by `make build`) in Chromium.
Zoom is played by webhooks signed exactly like Zoom signs them (`helpers/zoom.ts`), or by
`zoom-checker simulate`.

```bash
make build                    # once: web/dist + bin/zoom-checker (the frontend is embedded)
cd web
npx playwright install chromium   # once per machine
npm run e2e                   # = npx playwright test
npm run e2e -- acceptance.spec.ts -g S4   # one file / one test
npm run e2e:typecheck         # tsc over e2e/ and playwright.config.ts
```

`make e2e` does both steps from the repository root.

Results: `test-results/` (traces and screenshots of failures, `server.log` of the shared
server, `e2e-metrics.json` with the measured numbers: S1 latency webhook → «В Zoom» badge,
reconnect time, total suite time).

## How a run works

* `global-setup.ts` starts the binary once: fresh `DATA_DIR` under `$TMPDIR/zc-e2e-*`, a free
  port on 127.0.0.1 (`LISTEN_ADDR`, `PUBLIC_URL`), `ADMIN_PASSWORD`, `ZOOM_WEBHOOK_SECRET_TOKEN`,
  `TIMEZONE=Europe/Moscow`. It waits for `/healthz`; the function it returns is the global
  teardown (stops the server, writes the metrics, deletes the data).
* `auth.setup.ts` logs in through the login form once; the specs reuse that session
  (`e2e/.auth/admin.json`, git-ignored).
* Every test creates its own group (pasted list) and lesson with a random 11-digit Zoom
  meeting ID through the UI, so tests are independent and run in parallel.
* Two specs start **their own** server instance (always SQLite, own data directory):
  `admin.spec.ts` (first start without `ADMIN_PASSWORD`: generated password file, forced
  password change) and `live-reconnect.spec.ts` (kill -9 and restart while a live page is open).
* `retries: 0` — a flaky test is a bug to fix, not to retry.

| Spec | What |
|---|---|
| `acceptance.spec.ts` | TZ §36 scenarios S1–S9 (S1 measures webhook → badge latency, must be < 5 s) |
| `teacher.spec.ts` | TZ §8 duplicate names, cp1251 CSV import, lesson from a Zoom invitation, Был/Не был + «вручную», comment, «Не студент»/«Вернуть», finish/reopen, student card, group report, Zoom page, event journal |
| `admin.spec.ts` | generated first-admin password, local teacher account, settings |
| `simulator.spec.ts` | `zoom-checker simulate --speed 120 --duration 30m` while the live page is open |
| `live-reconnect.spec.ts` | server killed and restarted: «Подключаемся…» → «Обновление раз в 5 секунд» → live again |
| `layout.spec.ts` | tablet sizes (768, 820, 1024 px) and a small laptop (1280 px): no column of the live table cut off, no sideways page scroll |
| `auth/*.spec.ts` | project `auth`: Keycloak, OpenLDAP and Samba AD logins against `deploy/dev/auth-stand`, skipped without `E2E_AUTH_STAND` (see `auth/README.md`) |

## Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `E2E_DATABASE_URL` | empty | PostgreSQL URL for the shared server (`DATABASE_URL`). An empty database is best; reruns on the same one work too (every test uses unique names and meeting IDs). The two own-instance specs stay on SQLite |
| `E2E_WORKERS` | `2` | parallel browsers (keep it low on small machines) |
| `E2E_BIN` | `../bin/zoom-checker` | binary under test |
| `E2E_SIM_SEED` | random | seed for `simulate` (printed as a test annotation) |
| `E2E_KEEP_DATA` | empty | keep the run's data directory and print its path |
| `E2E_TMP` | `$TMPDIR` | where run directories are created |
| `E2E_LOG_LEVEL` | `info` | server `LOG_LEVEL` |
| `E2E_AUTH_STAND` | empty | set once `deploy/dev/auth-stand/stand.sh up` is done to run the `auth` project (needs port 8080 free) |

PostgreSQL run:

```bash
docker run -d --name zc-test-pg-e2e -e POSTGRES_PASSWORD=test -e POSTGRES_DB=zc -p 55433:5432 postgres:18-alpine
E2E_DATABASE_URL='postgres://postgres:test@127.0.0.1:55433/zc?sslmode=disable' npm run e2e
docker rm -f zc-test-pg-e2e
```

## Without browser libraries on the host

If `npx playwright install chromium` works but Chromium does not start (missing system
libraries and no sudo), run the suite inside the official image, which has the browser
and its libraries. The repository is mounted, the binary built on the host is used, and
`--network host` lets the browser reach the server started inside the container:

```bash
make build
cd web
npm run e2e:docker
# = docker run --rm --network host --ipc host --init -u "$(id -u):$(id -g)" -e HOME=/tmp \
#     -e E2E_DATABASE_URL -e E2E_WORKERS -v "$PWD/..:/work" -w /work/web \
#     mcr.microsoft.com/playwright:v1.63.0-noble npx playwright test
```

The image tag must match the `@playwright/test` version in `package.json` (1.63.x).
