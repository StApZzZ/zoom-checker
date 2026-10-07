#!/usr/bin/env bash
# Zoom Checker installer for Linux servers with systemd
# (Oracle/RHEL/Alma/Rocky/Fedora, Debian/Ubuntu, ...).
#
#   sudo ./install.sh                                  # interactive: asks the address, Secret Token (hidden), time zone
#   sudo ./install.sh --public-url https://att.example.com            # then paste the Secret Token when asked
#   sudo ./install.sh -y --public-url https://att.example.com --zoom-secret-file /root/zoom-token.txt
#   sudo ./install.sh --binary ./zoom-checker-linux-amd64   # upgrade: same command with a new binary
#   sudo ./install.sh --uninstall [--purge]
#
# Idempotent: re-running keeps existing settings and data, replaces the binary and restarts.
# Everything the administrator reads is in Russian; comments are in English.
set -euo pipefail

APP=zoom-checker
BIN_DST=/usr/local/bin/$APP
ETC_DIR=/etc/$APP
ENV_FILE=$ETC_DIR/$APP.env
DATA_DIR=/var/lib/$APP
UNIT_FILE=/etc/systemd/system/$APP.service
# Firewall ports this installer opened itself ("firewalld 443" / "ufw 443"), so
# --uninstall closes exactly those and never a rule the administrator added.
FW_STATE=$ETC_DIR/firewall-ports
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

BINARY="" ; BINARY_URL="" ; SHA256_ARG="" ; PUBLIC_URL_ARG="" ; TLS_ARG="" ; PORT_ARG=""
ZOOM_SECRET_ARG="" ; ZOOM_SECRET_FILE="" ; ZOOM_SECRET_STDIN=0 ; ZOOM_SECRET_ON_CMDLINE=0
TZ_ARG="" ; ASSUME_YES=0 ; UNINSTALL=0 ; PURGE=0

c_bold=$'\e[1m'; c_green=$'\e[32m'; c_yellow=$'\e[33m'; c_red=$'\e[31m'; c_off=$'\e[0m'
[ -t 1 ] || { c_bold=""; c_green=""; c_yellow=""; c_red=""; c_off=""; }
say()  { printf '%s==>%s %s\n' "$c_green" "$c_off" "$*"; }
warn() { printf '%s[!]%s %s\n' "$c_yellow" "$c_off" "$*" >&2; }
die()  { printf '%s[x]%s %s\n' "$c_red" "$c_off" "$*" >&2; exit 1; }

usage() {
  cat <<EOF
Установка Zoom Checker на сервер Linux с systemd.

Использование: sudo $0 [параметры]
Без параметров установщик сам спросит адрес сервиса, Secret Token из Zoom
(при вводе он не показывается на экране) и часовой пояс.

  --public-url URL         адрес сервиса в интернете, например https://attendance.example.com.
                           Для Zoom нужно доменное имя; IP-адрес (https://203.0.113.10)
                           годится только для проверки сервиса — Zoom его не примет
  --zoom-secret-file ФАЙЛ  взять Secret Token приложения Zoom из файла (первая строка)
  --zoom-secret -          прочитать Secret Token из стандартного ввода, например:
                           sudo $0 -y --zoom-secret - < token.txt
  --zoom-secret ТОКЕН      передать токен прямо в команде — не рекомендуется: его видно
                           в списке процессов, в истории команд и в журнале sudo
  --tls auto|off|file      auto — сервис сам получает сертификат Let's Encrypt, порты 80 и 443
                           (по умолчанию для адреса https://); off — обычный HTTP за прокси;
                           file — свой сертификат (TLS_CERT_FILE и TLS_KEY_FILE в файле настроек)
  --port N                 порт при --tls off или file (по умолчанию 8080; для file обычно 443)
  --timezone ПОЯС          часовой пояс сервиса, например Asia/Yekaterinburg
                           (по умолчанию — пояс сервера; Europe/Moscow, если на сервере UTC)
  --binary ПУТЬ            файл программы (по умолчанию — рядом с этим скриптом или в ./bin/)
  --url URL                скачать файл программы по адресу https://...
  --sha256 СУММА           проверить контрольную сумму файла программы (из SHA256SUMS)
  -y, --yes                ничего не спрашивать, взять значения по умолчанию
  --uninstall              остановить и удалить сервис (данные и настройки остаются)
  --purge                  вместе с --uninstall: удалить также $DATA_DIR и $ETC_DIR
  -h, --help               эта справка

Secret Token можно добавить и позже: впишите ZOOM_WEBHOOK_SECRET_TOKEN=... в $ENV_FILE
и запустите установщик ещё раз.
EOF
}

need_arg() { [ $# -ge 2 ] && [ -n "$2" ] || { usage >&2; die "параметру $1 нужно значение"; }; }
while [ $# -gt 0 ]; do
  case "$1" in
    --binary) need_arg "$@"; BINARY=$2; shift 2 ;;
    --url) need_arg "$@"; BINARY_URL=$2; shift 2 ;;
    --sha256) need_arg "$@"; SHA256_ARG=$2; shift 2 ;;
    --public-url) need_arg "$@"; PUBLIC_URL_ARG=$2; shift 2 ;;
    --zoom-secret)
      need_arg "$@"
      if [ "$2" = "-" ]; then ZOOM_SECRET_STDIN=1; else ZOOM_SECRET_ARG=$2; ZOOM_SECRET_ON_CMDLINE=1; fi
      shift 2 ;;
    --zoom-secret-file) need_arg "$@"; ZOOM_SECRET_FILE=$2; shift 2 ;;
    --tls) need_arg "$@"; TLS_ARG=$2; shift 2 ;;
    --port) need_arg "$@"; PORT_ARG=$2; shift 2 ;;
    --timezone) need_arg "$@"; TZ_ARG=$2; shift 2 ;;
    -y|--yes) ASSUME_YES=1; shift ;;
    --uninstall) UNINSTALL=1; shift ;;
    --purge) PURGE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "неизвестный параметр: $1" ;;
  esac
done

case "$PORT_ARG" in "" ) ;; *[!0-9]*) die "--port: нужно число, сейчас: $PORT_ARG" ;; esac
case "$TLS_ARG" in "" | auto | off | file) ;; *) die "--tls: можно указать auto, off или file, сейчас: $TLS_ARG" ;; esac
if [ -n "$SHA256_ARG" ]; then
  SHA256_ARG=$(printf '%s' "$SHA256_ARG" | tr 'A-F' 'a-f')
  case "$SHA256_ARG" in *[!0-9a-f]*) die "--sha256: нужна контрольная сумма из 64 символов 0-9 и a-f" ;; esac
  [ ${#SHA256_ARG} -eq 64 ] || die "--sha256: нужна контрольная сумма из 64 символов 0-9 и a-f"
fi
secret_sources=$((ZOOM_SECRET_STDIN + ZOOM_SECRET_ON_CMDLINE))
[ -z "$ZOOM_SECRET_FILE" ] || secret_sources=$((secret_sources + 1))
[ "$secret_sources" -le 1 ] || die "Secret Token укажите одним способом: --zoom-secret-file ФАЙЛ или --zoom-secret -"

[ "$(id -u)" -eq 0 ] || die "запустите установщик от имени root: sudo $0 ..."
command -v systemctl >/dev/null || die "нужен systemd: этот установщик работает на серверах Linux с systemd (иначе используйте Docker, см. deploy/README-docker.md)"

interactive() { [ "$ASSUME_YES" -eq 0 ] && [ -t 0 ]; }
ask() { # ask VAR "question" "default"
  local __var=$1 __q=$2 __def=${3:-} __ans=""
  if interactive; then
    read -r -p "$__q${__def:+ [$__def]}: " __ans || true
  fi
  printf -v "$__var" '%s' "${__ans:-$__def}"
}
# ask_secret VAR "question" — like ask, but the answer is not echoed, so it does
# not stay on the screen (or in a terminal recording) and never reaches argv.
ask_secret() {
  local __var=$1 __q=$2 __ans=""
  if interactive; then
    read -rs -p "$__q: " __ans || true
    printf '\n' >&2 # -s also swallows the newline of Enter
  fi
  printf -v "$__var" '%s' "$__ans"
}
# clean_secret VAR — drop what copy-paste adds (spaces, Windows line ends); fails
# when the token has characters the settings file cannot hold as a plain value.
clean_secret() {
  local __v=${!1}
  __v=${__v//$'\r'/}
  __v=${__v#"${__v%%[![:space:]]*}"}; __v=${__v%"${__v##*[![:space:]]}"}
  printf -v "$1" '%s' "$__v"
  case "$__v" in *[[:space:]\"\']*) return 1 ;; esac
}

# set_env KEY VALUE — set KEY=VALUE in the env file (value written verbatim): the
# first line for KEY (also a commented-out "#KEY=" template line) is replaced,
# later duplicates are dropped (the service would use the last one), otherwise
# the line is appended. The value goes through ENVIRON, not awk -v, which would
# eat backslashes (and not argv, where other users could read a secret).
set_env() {
  local key=$1 val=$2 tmp
  tmp=$(mktemp)
  if grep -qE "^[#[:space:]]*(export[[:space:]]+)?${key}[[:space:]]*=" "$ENV_FILE"; then
    K="$key" V="$val" awk 'BEGIN { k = ENVIRON["K"]; v = ENVIRON["V"]; done = 0 }
      $0 ~ ("^[#[:space:]]*(export[[:space:]]+)?" k "[[:space:]]*=") {
        if (!done) { print k "=" v; done = 1 }
        next
      }
      { print }' "$ENV_FILE" >"$tmp"
  else
    cp "$ENV_FILE" "$tmp"; printf '%s=%s\n' "$key" "$val" >>"$tmp"
  fi
  cat "$tmp" >"$ENV_FILE"; rm -f "$tmp"
}
# get_env KEY — the value the service will use: last active line wins, surrounding
# quotes removed, unquoted " # comment" tails dropped (as zoom-checker --config reads it).
get_env() {
  local line v q rest after
  line=$(grep -E "^[[:space:]]*(export[[:space:]]+)?$1[[:space:]]*=" "$ENV_FILE" 2>/dev/null | tail -n 1) || true
  [ -n "$line" ] || return 0
  v=${line#*=}
  v=${v#"${v%%[![:space:]]*}"}; v=${v%"${v##*[![:space:]]}"}
  case "$v" in
    \"*\" | \'*\') v=${v:1:${#v}-2} ;;
    \"*\"[[:space:]]*'#'* | \'*\'[[:space:]]*'#'*)
      # "value"  # comment → value; anything but whitespace after the closing
      # quote → read as an unquoted value, like zoom-checker --config does.
      q=${v:0:1}; rest=${v:1}; after=${rest#*"$q"}; after=${after#"${after%%[![:space:]]*}"}
      if [ "${rest#*"$q"}" != "$after" ] && [ "${after:0:1}" = "#" ]; then
        v=${rest%%"$q"*}
      else
        v=${v%% #*}; v=${v%"${v##*[![:space:]]}"}
      fi ;;
    *' #'*) v=${v%% #*}; v=${v%"${v##*[![:space:]]}"} ;;
  esac
  printf '%s\n' "$v"
}

sha256_of() { sha256sum <"$1" | cut -d' ' -f1; }
# verify_sums FILE — when a SHA256SUMS (from `make release`) lies next to FILE and
# lists it, the checksum must match: catches a partial or corrupted copy before
# root runs or installs it. Silent when there is nothing to compare with.
verify_sums() {
  local f=$1 dir name want
  dir=$(cd "$(dirname "$f")" && pwd); name=$(basename "$f")
  [ -f "$dir/SHA256SUMS" ] || return 0
  want=$(awk -v n="$name" '$2 == n || $2 == "*" n { print tolower($1); exit }' "$dir/SHA256SUMS")
  [ -n "$want" ] || return 0
  [ "$(sha256_of "$f")" = "$want" ] ||
    die "файл $f повреждён или скопирован не полностью (контрольная сумма не совпадает с SHA256SUMS): скопируйте пакет установки ещё раз"
  say "контрольная сумма $name совпала с SHA256SUMS"
}

# server_tz — the server's time zone name, or empty. timedatectl needs D-Bus
# (absent in minimal images and containers), so the /etc/localtime link is the fallback.
server_tz() {
  local tz=""
  tz=$(timedatectl show -p Timezone --value 2>/dev/null) || tz=""
  if [ -z "$tz" ] && [ -L /etc/localtime ]; then
    tz=$(readlink /etc/localtime 2>/dev/null) || tz=""
    case "$tz" in */zoneinfo/*) tz=${tz#*/zoneinfo/}; tz=${tz#posix/}; tz=${tz#right/} ;; *) tz="" ;; esac
  fi
  if [ -z "$tz" ] && [ -r /etc/timezone ]; then tz=$(head -n 1 /etc/timezone); fi
  printf '%s\n' "$tz"
}
is_utc() { case "$1" in "" | UTC | Etc/UTC | Universal | Etc/Universal | Zulu | Etc/Zulu | GMT | Etc/GMT | Etc/GMT0 | GMT0) return 0 ;; esac; return 1; }
# known_tz NAME — false only when the system tz database is present and lacks NAME
# (the binary carries its own copy; check-config has the final say).
known_tz() {
  case "$1" in "" | /* | *..*) return 1 ;; esac
  [ ! -d /usr/share/zoneinfo ] || [ -f "/usr/share/zoneinfo/$1" ]
}

# service_ports TLS_MODE PUBLIC_URL LISTEN_ADDR — TCP ports the service needs
# reachable from outside (none when it listens on loopback behind a local proxy).
service_ports() {
  local tls=${1,,} url=$2 listen=${3:-:8080} p
  if [ "$tls" = auto ]; then
    p=$(printf '%s\n' "$url" | sed -nE 's#^https://(\[[^]]*\]|[^/:]+):([0-9]+).*#\2#p')
    echo "80 ${p:-443}"
  else
    case "$listen" in
      127.* | localhost:* | "[::1]:"*) ;;
      *) echo "${listen##*:}" ;;
    esac
  fi
}

# ── firewall ─────────────────────────────────────────────────────────────────
# Command output is captured before matching: `cmd | grep -q` under pipefail
# turns into "no match" when grep exits early and cmd dies of SIGPIPE.
fw_active() { # the running host firewall: firewalld, ufw or nothing
  if command -v firewall-cmd >/dev/null && systemctl is-active --quiet firewalld 2>/dev/null; then
    echo firewalld
  elif command -v ufw >/dev/null; then
    # LC_ALL=C: ufw translates "Status: active" on Russian-language servers.
    case "$(LC_ALL=C ufw status 2>/dev/null)" in "Status: active"*) echo ufw ;; esac
  fi
}
fw_is_open() { # fw_is_open TOOL PORT — a permanent rule for PORT/tcp exists
  local added
  case "$1" in
    firewalld) firewall-cmd --permanent --query-port="$2/tcp" >/dev/null 2>&1 ;;
    ufw) added=$(LC_ALL=C ufw show added 2>/dev/null) || true
         grep -qxF "ufw allow $2/tcp" <<<"$added" ;;
    *) return 1 ;;
  esac
}
fw_recorded() { [ -f "$FW_STATE" ] && grep -qxF "$1 $2" "$FW_STATE"; }
fw_close_cmd() { # the command an administrator runs to close PORT/tcp
  case "$1" in
    firewalld) echo "sudo firewall-cmd --permanent --remove-port=$2/tcp && sudo firewall-cmd --reload" ;;
    ufw) echo "sudo ufw delete allow $2/tcp" ;;
  esac
}
# fw_allow PORT... — open the ports in firewalld/ufw. A port that is already open
# is left alone and not recorded: that rule belongs to the administrator.
fw_allow() {
  local tool p opened=() failed=0
  [ $# -gt 0 ] || return 0 # loopback only: nothing to open
  tool=$(fw_active)
  if [ -z "$tool" ]; then
    warn "файрвол на сервере не найден: проверьте, что порты $* (TCP) открыты из интернета (например, в группе безопасности облака)"
    return 0
  fi
  for p in "$@"; do
    if fw_is_open "$tool" "$p"; then continue; fi
    case "$tool" in
      # Permanent rule plus the same rule at runtime: no --reload, which would drop
      # runtime-only rules the administrator added by hand.
      firewalld)
        if firewall-cmd --quiet --permanent --add-port="$p/tcp"; then
          firewall-cmd --quiet --add-port="$p/tcp" || true
        else
          warn "firewalld: не удалось открыть порт $p (TCP), откройте его сами: sudo firewall-cmd --permanent --add-port=$p/tcp && sudo firewall-cmd --reload"
          failed=1; continue
        fi ;;
      ufw)
        if ! ufw allow "$p/tcp" >/dev/null; then
          warn "ufw: не удалось открыть порт $p (TCP), откройте его сами: sudo ufw allow $p/tcp"
          failed=1; continue
        fi ;;
    esac
    fw_recorded "$tool" "$p" || printf '%s %s\n' "$tool" "$p" >>"$FW_STATE"
    opened+=("$p")
  done
  if [ ${#opened[@]} -gt 0 ]; then say "$tool: открыты порты ${opened[*]} (TCP)"
  elif [ "$failed" -eq 0 ]; then say "$tool: порты $* (TCP) уже открыты"; fi
}
port_in_use() { # another program listens on PORT/tcp (checked after our service stopped)
  command -v ss >/dev/null || return 1
  [ -n "$(ss -ltn "( sport = :$1 )" 2>/dev/null | tail -n +2)" ]
}
# fw_close_recorded — close the ports this installer opened; returns the list of
# ports it handled in FW_HANDLED.
FW_HANDLED=" "
fw_close_recorded() {
  local tool p
  [ -f "$FW_STATE" ] || return 0
  # fd 3, not stdin: firewall tools run inside the loop and must not eat the list.
  while read -r tool p <&3; do
    case "$tool" in firewalld | ufw) ;; *) continue ;; esac
    case "$p" in "" | *[!0-9]*) continue ;; esac
    FW_HANDLED+="$p "
    if port_in_use "$p"; then
      warn "порт $p (TCP) оставлен открытым: он нужен другой программе на этом сервере. Если это не так, закройте его: $(fw_close_cmd "$tool" "$p")"
      continue
    fi
    if [ "$tool" = firewalld ] && command -v firewall-cmd >/dev/null && systemctl is-active --quiet firewalld 2>/dev/null; then
      firewall-cmd --quiet --permanent --remove-port="$p/tcp" || true
      firewall-cmd --quiet --remove-port="$p/tcp" || true
      say "firewalld: закрыт порт $p (TCP)"
    elif [ "$tool" = ufw ] && command -v ufw >/dev/null; then
      ufw --force delete allow "$p/tcp" >/dev/null 2>&1 || true
      say "ufw: закрыт порт $p (TCP)"
    else
      warn "не удалось закрыть порт $p (TCP): $tool не запущен. Закройте его сами: $(fw_close_cmd "$tool" "$p")"
    fi
  done 3<"$FW_STATE"
  rm -f "$FW_STATE"
}

# ── uninstall ────────────────────────────────────────────────────────────────
if [ "$UNINSTALL" -eq 1 ]; then
  used_ports=""
  if [ -f "$ENV_FILE" ]; then
    used_ports=$(service_ports "$(get_env TLS_MODE)" "$(get_env PUBLIC_URL)" "$(get_env LISTEN_ADDR)")
  fi
  systemctl disable --now "$APP" 2>/dev/null || true
  fw_close_recorded
  # Ports the service used that are open, but not by this installer (by hand or by
  # an installer version that did not record them): say exactly how to close them.
  tool=$(fw_active)
  cloud_ports=()
  for p in $used_ports; do
    case "$FW_HANDLED" in *" $p "*) continue ;; esac
    if [ -n "$tool" ]; then
      if fw_is_open "$tool" "$p" && ! port_in_use "$p"; then
        warn "порт $p (TCP) открыт в $tool не этим установщиком. Если он нужен был только Zoom Checker, закройте его: $(fw_close_cmd "$tool" "$p")"
      fi
    else
      cloud_ports+=("$p")
    fi
  done
  if [ ${#cloud_ports[@]} -gt 0 ]; then
    warn "если вы открывали порты ${cloud_ports[*]} (TCP) для Zoom Checker в облаке (группа безопасности) или на внешнем файрволе, закройте их там"
  fi
  rm -f "$UNIT_FILE" "$BIN_DST"
  systemctl daemon-reload
  if [ "$PURGE" -eq 1 ]; then
    rm -rf "$DATA_DIR" "$ETC_DIR"
    userdel "$APP" 2>/dev/null || true
    say "удалены сервис, программа, настройки и данные"
  else
    say "удалены сервис и программа; настройки остались в $ETC_DIR, данные — в $DATA_DIR"
  fi
  exit 0
fi

# ── Secret Token from a file or stdin (never through argv) ───────────────────
if [ -n "$ZOOM_SECRET_FILE" ]; then
  [ -r "$ZOOM_SECRET_FILE" ] || die "--zoom-secret-file: не удаётся прочитать файл $ZOOM_SECRET_FILE"
  IFS= read -r ZOOM_SECRET_ARG <"$ZOOM_SECRET_FILE" || true # a last line without "\n" still counts
elif [ "$ZOOM_SECRET_STDIN" -eq 1 ]; then
  if [ -t 0 ]; then
    read -rs -p "Secret Token приложения Zoom (при вводе не отображается): " ZOOM_SECRET_ARG || true
    printf '\n' >&2
  else
    IFS= read -r ZOOM_SECRET_ARG || true # stdin is then used up: interactive() is false from here on
  fi
fi
if [ -n "$ZOOM_SECRET_FILE" ] || [ "$ZOOM_SECRET_STDIN" -eq 1 ] || [ "$ZOOM_SECRET_ON_CMDLINE" -eq 1 ]; then
  clean_secret ZOOM_SECRET_ARG || die "в Secret Token не должно быть пробелов и кавычек: скопируйте его из приложения Zoom ещё раз"
  [ -n "$ZOOM_SECRET_ARG" ] || die "Secret Token пустой: скопируйте его из приложения Zoom (вкладка Feature → Secret Token)"
fi
if [ "$ZOOM_SECRET_ON_CMDLINE" -eq 1 ]; then
  warn "Secret Token, переданный в командной строке, видят другие пользователи сервера (список процессов), он остаётся в истории команд и журнале sudo. В следующий раз запустите установщик без --zoom-secret и вставьте токен, когда он спросит, или используйте --zoom-secret-file ФАЙЛ"
fi

# ── binary ───────────────────────────────────────────────────────────────────
if [ -n "$BINARY_URL" ]; then
  case "$BINARY_URL" in https://*) ;; *) die "--url: нужен адрес, начинающийся с https://, сейчас: $BINARY_URL" ;; esac
  command -v curl >/dev/null || die "для --url нужна программа curl: установите её или скачайте файл сами и укажите --binary ПУТЬ"
  BINARY=$(mktemp)
  trap 'rm -f "$BINARY"' EXIT
  say "скачиваю $BINARY_URL"
  # https only, also after redirects: root runs and installs what comes back.
  curl --proto '=https' --proto-redir '=https' -fsSL "$BINARY_URL" -o "$BINARY" ||
    die "не удалось скачать файл программы: $BINARY_URL"
fi
if [ -z "$BINARY" ]; then
  arch=$(uname -m); case "$arch" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac
  # Last resort: the installed binary (a rerun that only changes settings).
  for cand in "$SCRIPT_DIR/$APP" "$SCRIPT_DIR/$APP-linux-$arch" "$SCRIPT_DIR/../bin/$APP" "./$APP" "./$APP-linux-$arch" "./bin/$APP" "$BIN_DST"; do
    if [ -f "$cand" ]; then BINARY=$cand; break; fi
  done
fi
[ -n "$BINARY" ] && [ -f "$BINARY" ] ||
  die "не найден файл программы zoom-checker: положите его рядом с install.sh или укажите --binary ПУТЬ (или --url https://...)"
# Integrity before the first run as root: an explicit --sha256 always; otherwise
# SHA256SUMS next to a copied file. A download without --sha256 only gets a warning.
if [ -n "$SHA256_ARG" ]; then
  [ "$(sha256_of "$BINARY")" = "$SHA256_ARG" ] ||
    die "контрольная сумма $BINARY не совпадает с --sha256: файл повреждён или подменён, не устанавливаю"
  say "контрольная сумма совпала"
elif [ -n "$BINARY_URL" ]; then
  warn "контрольная сумма скачанного файла не проверена: укажите --sha256 <сумма из SHA256SUMS>"
elif ! [ "$BINARY" -ef "$BIN_DST" ]; then
  verify_sums "$BINARY"
fi
[ -x "$BINARY" ] || chmod +x "$BINARY"
"$BINARY" version >/dev/null 2>&1 ||
  die "$BINARY — не программа zoom-checker для этого сервера (другая архитектура процессора или файл повреждён)"

# ── user and directories ─────────────────────────────────────────────────────
if ! id "$APP" >/dev/null 2>&1; then
  nologin=$(command -v nologin || echo /sbin/nologin)
  useradd --system --home-dir "$DATA_DIR" --no-create-home --shell "$nologin" --user-group "$APP"
  say "создан системный пользователь $APP"
fi
install -d -m 0750 -o root -g "$APP" "$ETC_DIR"
install -d -m 0750 -o "$APP" -g "$APP" "$DATA_DIR"

# `install` (not mv) gives the right SELinux label (bin_t) automatically.
if systemctl is-active --quiet "$APP"; then UPGRADE=1; else UPGRADE=0; fi
[ "$BINARY" -ef "$BIN_DST" ] || install -m 0755 -o root -g root "$BINARY" "$BIN_DST"
command -v restorecon >/dev/null && restorecon -q "$BIN_DST" || true
say "установлена программа $APP $("$BIN_DST" version)"

# ── settings ─────────────────────────────────────────────────────────────────
FIRST_INSTALL=0
if [ ! -f "$ENV_FILE" ]; then
  FIRST_INSTALL=1
  if [ -f "$SCRIPT_DIR/.env.example" ]; then
    install -m 0640 -o root -g "$APP" "$SCRIPT_DIR/.env.example" "$ENV_FILE"
  else
    install -m 0640 -o root -g "$APP" /dev/null "$ENV_FILE"
    printf '# Настройки Zoom Checker, описание: docs/admin-guide.md\n' >"$ENV_FILE"
  fi
fi

current_url=$(get_env PUBLIC_URL)
if [ -n "$PUBLIC_URL_ARG" ]; then
  PUBLIC_URL=$PUBLIC_URL_ARG
elif [ "$FIRST_INSTALL" -eq 1 ] || [ -z "$current_url" ] || [ "$current_url" = "https://attendance.example.com" ]; then
  # Public IP as seen from the internet (what Zoom will call), else the address
  # of the default route. Never fatal: minimal images lack `hostname` or `ip`.
  guess_ip=$(curl -fsS -m 5 https://api.ipify.org 2>/dev/null) || guess_ip=""
  case "$guess_ip" in *[!0-9a-fA-F.:]* | "") guess_ip="" ;; esac
  if [ -z "$guess_ip" ] && command -v ip >/dev/null; then
    guess_ip=$(ip -4 -o route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p') || guess_ip=""
  fi
  if [ -z "$guess_ip" ] && command -v hostname >/dev/null; then
    guess_ip=$(hostname -I 2>/dev/null | awk '{print $1}') || guess_ip=""
  fi
  ask PUBLIC_URL "Адрес сервиса в интернете — доменное имя (IP-адрес Zoom не примет, он годится только для проверки)" "https://${guess_ip:-localhost}"
else
  PUBLIC_URL=$current_url
fi
case "$PUBLIC_URL" in http://*|https://*) ;; *) PUBLIC_URL="https://$PUBLIC_URL" ;; esac
PUBLIC_URL=${PUBLIC_URL%/}
set_env PUBLIC_URL "$PUBLIC_URL"

if [ -n "$TLS_ARG" ]; then TLS_MODE=$TLS_ARG
elif [ "$FIRST_INSTALL" -eq 1 ] || [ -z "$(get_env TLS_MODE)" ]; then
  case "$PUBLIC_URL" in https://*) TLS_MODE=auto ;; *) TLS_MODE=off ;; esac
else TLS_MODE=$(get_env TLS_MODE); fi
TLS_MODE=${TLS_MODE,,} # the service reads it case-insensitively
set_env TLS_MODE "$TLS_MODE"
# LISTEN_ADDR (TLS_MODE off/file): --port changes only the port, so an address
# the administrator pinned (127.0.0.1:8080 behind a local proxy) stays private.
listen=$(get_env LISTEN_ADDR)
if [ "$TLS_MODE" != "auto" ]; then
  if [ -n "$PORT_ARG" ]; then
    case "$listen" in *:*) listen="${listen%:*}:$PORT_ARG" ;; *) listen=":$PORT_ARG" ;; esac
    set_env LISTEN_ADDR "$listen"
  elif [ -z "$listen" ]; then
    listen=":8080"
    set_env LISTEN_ADDR "$listen"
  fi
fi

if [ -n "$ZOOM_SECRET_ARG" ]; then
  set_env ZOOM_WEBHOOK_SECRET_TOKEN "$ZOOM_SECRET_ARG"
elif [ -z "$(get_env ZOOM_WEBHOOK_SECRET_TOKEN)" ]; then
  while :; do
    ask_secret zs "Secret Token приложения Zoom (при вставке не отображается; Enter — добавить позже)"
    if clean_secret zs; then break; fi
    warn "в Secret Token не должно быть пробелов и кавычек: скопируйте его из приложения Zoom ещё раз"
  done
  if [ -n "$zs" ]; then
    set_env ZOOM_WEBHOOK_SECRET_TOKEN "$zs"
    say "Secret Token сохранён (длина: ${#zs})"
  fi
fi

systz=$(server_tz)
if [ -n "$TZ_ARG" ]; then set_env TIMEZONE "$TZ_ARG"
elif [ -z "$(get_env TIMEZONE)" ]; then
  # The server's zone is usually the school's; cloud VMs run on UTC, which is
  # never what a Russian school wants, so they get Moscow — and a question.
  def_tz=$systz
  if is_utc "$def_tz"; then def_tz=Europe/Moscow; fi
  tz=$def_tz
  while :; do
    ask tz "Часовой пояс сервиса — для расписания и времени в сообщениях (например, Asia/Yekaterinburg)" "$def_tz"
    if ! interactive || known_tz "$tz"; then break; fi
    warn "неизвестный часовой пояс «$tz»: введите название вида Europe/Moscow или Asia/Novosibirsk"
  done
  set_env TIMEZONE "$tz"
fi
chown root:"$APP" "$ENV_FILE"; chmod 0640 "$ENV_FILE"

# zoom-checker as the service runs it: service user, only DATA_DIR from the unit
# in the environment (a variable exported in this shell would override the file).
svc_cli() {
  runuser -u "$APP" -- env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin DATA_DIR="$DATA_DIR" \
    "$BIN_DST" --config "$ENV_FILE" "$@"
}

# Validate before (re)starting so a typo never takes the running service down.
if ! svc_cli check-config >/dev/null 2>&1; then
  svc_cli check-config || true
  die "в настройках $ENV_FILE есть ошибки (см. выше): исправьте их и запустите установщик ещё раз"
fi

# ── systemd ──────────────────────────────────────────────────────────────────
if [ -f "$SCRIPT_DIR/$APP.service" ]; then
  install -m 0644 "$SCRIPT_DIR/$APP.service" "$UNIT_FILE"
else
  cat >"$UNIT_FILE" <<'UNIT'
[Unit]
Description=Zoom Checker — учёт посещаемости Zoom-занятий
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=zoom-checker
Group=zoom-checker
# The service reads its settings file itself (--config), with the same parser
# as `zoom-checker check-config`, which install.sh runs before every restart.
# systemd's EnvironmentFile= has other rules (no inline "# comments", other
# quoting), so a file it accepted could still fail here, or the reverse.
Environment=DATA_DIR=/var/lib/zoom-checker
ExecStart=/usr/local/bin/zoom-checker --config /etc/zoom-checker/zoom-checker.env serve
Restart=on-failure
RestartSec=3
TimeoutStopSec=20

# Bind 80/443 without running as root.
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

# Hardening.
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectClock=true
ProtectHostname=true
ProtectProc=invisible
RestrictSUIDSGID=true
RestrictRealtime=true
RestrictNamespaces=true
RemoveIPC=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources
SystemCallErrorNumber=EPERM
StateDirectory=zoom-checker
StateDirectoryMode=0750
ReadWritePaths=/var/lib/zoom-checker
UMask=0027
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT
fi
systemctl daemon-reload

# shellcheck disable=SC2046 # one word per port
fw_allow $(service_ports "$TLS_MODE" "$PUBLIC_URL" "$listen")

systemctl enable --quiet "$APP"
systemctl restart "$APP"
say "жду запуска сервиса..."
ok=0
for _ in $(seq 1 40); do
  # The binary knows where it listens (port 80 in auto mode, https in file mode).
  if svc_cli healthcheck --timeout 2s >/dev/null 2>&1; then ok=1; break; fi
  sleep 0.5
done
if [ "$ok" -ne 1 ]; then
  journalctl -u "$APP" -n 40 --no-pager >&2 || true
  die "сервис не запустился — причина в журнале выше (полностью: journalctl -u $APP)"
fi

# ── summary ──────────────────────────────────────────────────────────────────
tz=$(get_env TIMEZONE); tz=${tz:-Europe/Moscow}
echo
printf '%sZoom Checker %s работает%s\n\n' "$c_bold" "$("$BIN_DST" version)" "$c_off"
printf '  Открыть в браузере:      %s\n' "$PUBLIC_URL"
printf '  Адрес для Zoom (Event notification endpoint URL):\n'
printf '                           %s/api/webhooks/zoom\n' "$PUBLIC_URL"
printf '  Файл настроек:           %s\n' "$ENV_FILE"
printf '  Данные (база, копии):    %s\n' "$DATA_DIR"
printf '  Журнал:                  journalctl -u %s -f\n' "$APP"
ext_ports=$(service_ports "$TLS_MODE" "$PUBLIC_URL" "$listen")
if [ -n "$ext_ports" ]; then
  # The health check above ran from inside the server; Zoom and browsers come from outside.
  printf '  Проверка снаружи:        откройте %s/healthz с другого компьютера — должно быть «ok».\n' "$PUBLIC_URL"
  printf '                           Если не открывается, откройте порты %s (TCP) в группе безопасности облака.\n' "$ext_ports"
fi
if [ -n "$systz" ] && ! is_utc "$systz" && [ "$systz" != "$tz" ]; then
  printf '  Часовой пояс сервиса:    %s (на сервере — %s; изменить: sudo %s --timezone ПОЯС)\n' "$tz" "$systz" "$0"
else
  printf '  Часовой пояс сервиса:    %s (изменить: sudo %s --timezone ПОЯС)\n' "$tz" "$0"
fi
if [ -f "$DATA_DIR/initial-admin-password.txt" ]; then
  printf '\n  Первый вход:  %s / %s\n' "$(get_env ADMIN_USERNAME | grep . || echo admin)" "$(cat "$DATA_DIR/initial-admin-password.txt")"
  printf '  (при первом входе нужно сменить пароль; после этого файл с паролем удаляется)\n'
fi
if [ "$TLS_MODE" = "auto" ]; then
  case "$PUBLIC_URL" in
    https://localhost* | https://127.* | "https://[::1]"*)
      echo; warn "Let's Encrypt не выдаёт сертификаты для адреса $PUBLIC_URL: запустите установщик ещё раз с --public-url https://<домен или внешний IP-адрес>" ;;
  esac
fi
# Zoom accepts the event endpoint only with a domain name (check-config warns too).
url_host=${PUBLIC_URL#*://}; url_host=${url_host%%/*}
case "$url_host" in
  127.* | "[::1]"*) ;;
  \[*) url_ip=1 ;;
  *[!0-9.:]*) url_ip=0 ;;
  [0-9]*) url_ip=1 ;;
  *) url_ip=0 ;;
esac
if [ "${url_ip:-0}" -eq 1 ]; then
  echo; warn "адрес $PUBLIC_URL — IP-адрес: сервис работает, но Zoom принимает адрес для событий только с доменным именем. Заведите DNS-запись на этот сервер и запустите установщик ещё раз с --public-url https://<домен>"
fi
if [ -z "$(get_env ZOOM_WEBHOOK_SECRET_TOKEN)" ]; then
  echo; warn "Secret Token из Zoom ещё не задан — без него сервис не принимает события Zoom. Запустите установщик ещё раз без параметров (sudo $0) и вставьте токен, когда он спросит, или впишите ZOOM_WEBHOOK_SECRET_TOKEN=... в $ENV_FILE и запустите установщик ещё раз"
fi
if [ "$UPGRADE" -eq 1 ]; then say "обновление завершено (настройки и данные сохранены)"; fi
exit 0
