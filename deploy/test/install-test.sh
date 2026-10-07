#!/usr/bin/env bash
# End-to-end test of deploy/install.sh in a throwaway systemd container
# (Ubuntu 24.04, real ufw; firewalld is a stand-in because real firewalld needs
# kernel features many CI/WSL kernels lack). Run: make test-install
#
# Needs Docker and a built ./bin/zoom-checker (make build). The container is
# privileged (systemd + iptables) and has no network. It is always removed;
# the image is removed too unless ZC_KEEP_IMAGE=1.
# shellcheck disable=SC2016 # single-quoted $... expands in the container's shell
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
IMAGE=zc-test-install:ubuntu24
CTR=zc-test-install
BIN=$ROOT/bin/zoom-checker

if ! command -v docker >/dev/null || ! docker info >/dev/null 2>&1; then
  echo "SKIP: Docker is not available"; exit 0
fi
[ -x "$BIN" ] || { echo "FAIL: $BIN not found, run: make build" >&2; exit 1; }

WORK=$(mktemp -d)
cleanup() {
  docker rm -f "$CTR" >/dev/null 2>&1 || true
  [ "${ZC_KEEP_IMAGE:-0}" = 1 ] || docker rmi "$IMAGE" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

PASS=0; FAIL=0
ok()   { PASS=$((PASS + 1)); printf '  ok    %s\n' "$1"; }
bad()  { FAIL=$((FAIL + 1)); printf '  FAIL  %s\n' "$1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | sed 's/^/        | /' | tail -n 30; }
# check DESC CMD... — CMD runs on the host; pass when it exits 0.
check() { local d=$1; shift; if "$@"; then ok "$d"; else bad "$d" "${OUT:-}"; fi; }
x()  { docker exec "$CTR" "$@"; }            # run in the container
xi() { docker exec -i "$CTR" "$@"; }         # ... with this script's stdin (heredocs)
has()    { grep -qF -- "$1" <<<"$OUT"; }      # last output contains
hasnt()  { ! grep -qF -- "$1" <<<"$OUT"; }
# run CMD — run a shell command in the container, keep output (colors stripped) and status.
run() {
  set +e; OUT=$(docker exec "$CTR" bash -c "$1" 2>&1); RC=$?; set -e
  # shellcheck disable=SC2001 # a regex; ${OUT//...} only takes globs
  OUT=$(sed 's/\x1b\[[0-9;]*m//g' <<<"$OUT")
}
envv() { x bash -c "grep -E '^$1=' /etc/zoom-checker/zoom-checker.env | tail -n 1 | cut -d= -f2-"; }
fwstate() { x bash -c 'cat /etc/zoom-checker/firewall-ports 2>/dev/null || true'; }
ufw_added() { x bash -c 'LC_ALL=C ufw show added'; }

echo "== image"
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  docker build -q -t "$IMAGE" - >/dev/null <<'EOF'
FROM ubuntu:24.04
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus ufw iproute2 curl ca-certificates tzdata python3 \
 && apt-get clean && rm -rf /var/lib/apt/lists/* \
 && systemctl mask getty@tty1.service console-getty.service systemd-resolved.service
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
EOF
fi

echo "== bundle (as make release lays it out)"
arch=$(uname -m); case "$arch" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac
B=$WORK/bundle; mkdir -p "$B"
cp "$BIN" "$B/zoom-checker-linux-$arch"
install -m 0755 "$ROOT/deploy/install.sh" "$B/install.sh"
cp "$ROOT/deploy/zoom-checker.service" "$ROOT/deploy/.env.example" "$B/"
(cd "$B" && sha256sum "zoom-checker-linux-$arch" install.sh zoom-checker.service .env.example >SHA256SUMS)
SUM=$(sha256sum "$B/zoom-checker-linux-$arch" | cut -d' ' -f1)

docker rm -f "$CTR" >/dev/null 2>&1 || true
# cgroup v1 hosts need the host cgroup tree; on cgroup v2 a private namespace is enough.
if [ "$(stat -fc %T /sys/fs/cgroup)" = cgroup2fs ]; then cg=(--cgroupns=private)
else cg=(--cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw); fi
docker run -d --name "$CTR" --privileged "${cg[@]}" --network none \
  --tmpfs /run --tmpfs /run/lock -e container=docker "$IMAGE" >/dev/null
for _ in $(seq 1 30); do
  st=$(docker exec "$CTR" systemctl is-system-running 2>/dev/null || true)
  case "$st" in running | degraded) break ;; esac; sleep 1
done
docker cp "$B" "$CTR:/root/bundle" >/dev/null
# The server is in Yekaterinburg; ufw is on, with SSH allowed by the administrator.
x bash -c 'timedatectl set-timezone Asia/Yekaterinburg && ufw --force enable >/dev/null && ufw allow 22/tcp >/dev/null'
# Without timedatectl (no D-Bus in minimal images) the zone comes from the /etc/localtime link.
OUT=$(x bash -c 'mkdir -p /root/nobin && ln -sf /usr/bin/readlink /usr/bin/head /root/nobin/ &&
  PATH=/root/nobin /bin/bash -c "$(sed -n "/^server_tz() {/,/^}/p" /root/bundle/install.sh); server_tz"') || true
check "server zone found without timedatectl" test "$OUT" = Asia/Yekaterinburg

echo "== secret from stdin, server time zone, SHA256SUMS, ufw"
run "cd /root/bundle && printf '  tok_Stdin123\r\n' | ./install.sh -y --public-url http://10.0.0.5:8080 --tls off --zoom-secret -"
check "first install succeeds" test "$RC" -eq 0
check "secret read from stdin, spaces and CR dropped" test "$(envv ZOOM_WEBHOOK_SECRET_TOKEN)" = tok_Stdin123
check "TIMEZONE = the server's zone (not the template's Moscow)" test "$(envv TIMEZONE)" = Asia/Yekaterinburg
check "exactly one TIMEZONE line, in place of the template line" \
  test "$(x grep -c 'TIMEZONE=' /etc/zoom-checker/zoom-checker.env)" = 1
check "bundle binary checked against SHA256SUMS" has "контрольная сумма zoom-checker-linux-$arch совпала"
check "summary is in Russian" has "Открыть в браузере:"
check "summary shows the time zone" has "Часовой пояс сервиса:    Asia/Yekaterinburg"
check "no command-line secret warning" hasnt "видят другие пользователи"
check "no English left in the summary" hasnt "Open in browser"
check "ufw rule for 8080 added" grep -qx 'ufw allow 8080/tcp' <<<"$(ufw_added)"
check "installer records the port it opened" test "$(fwstate)" = "ufw 8080"
check "service is active" x systemctl is-active --quiet zoom-checker

echo "== secret on the command line (compatibility) and from a file"
run "cd /root/bundle && ./install.sh -y --zoom-secret tok_Cmdline456"
check "--zoom-secret TOKEN still works" test "$RC" -eq 0 -a "$(envv ZOOM_WEBHOOK_SECRET_TOKEN)" = tok_Cmdline456
check "--zoom-secret TOKEN warns about the process list and history" has "видят другие пользователи сервера"
x bash -c "printf 'tok_File789' >/root/zoom-token.txt && chmod 600 /root/zoom-token.txt"
run "cd /root/bundle && ./install.sh -y --zoom-secret-file /root/zoom-token.txt"
check "--zoom-secret-file (no final newline)" test "$RC" -eq 0 -a "$(envv ZOOM_WEBHOOK_SECRET_TOKEN)" = tok_File789
check "--zoom-secret-file gives no warning" hasnt "видят другие пользователи"
check "rerun keeps the time zone" test "$(envv TIMEZONE)" = Asia/Yekaterinburg
check "rerun does not record 8080 twice" test "$(fwstate)" = "ufw 8080"
run "cd /root/bundle && echo x | ./install.sh -y --zoom-secret - --zoom-secret-file /root/zoom-token.txt"
check "two secret sources are refused" test "$RC" -ne 0
check "  ... with a Russian message" has "одним способом"
run "cd /root/bundle && ./install.sh -y --zoom-secret 'tok with space'"
check "a secret with spaces is refused" test "$RC" -ne 0 -a "$(envv ZOOM_WEBHOOK_SECRET_TOKEN)" = tok_File789

echo "== binary integrity"
pid=$(x systemctl show -p MainPID --value zoom-checker)
x bash -c "rm -rf /root/broken && cp -a /root/bundle /root/broken && truncate -s 1M /root/broken/zoom-checker-linux-$arch"
run "cd /root/broken && ./install.sh -y"
check "partial copy refused by SHA256SUMS" test "$RC" -ne 0
check "  ... with a Russian message" has "повреждён или скопирован не полностью"
check "  ... and the running service is untouched" test "$(x systemctl show -p MainPID --value zoom-checker)" = "$pid"
run "cd /root/bundle && ./install.sh -y --sha256 $(printf '0%.0s' $(seq 64))"
check "wrong --sha256 refused" test "$RC" -ne 0
check "  ... with a Russian message" has "не совпадает с --sha256"
run "cd /root/bundle && ./install.sh -y --sha256 ${SUM^^}"
check "right --sha256 (any case) accepted" test "$RC" -eq 0
run "/root/bundle/install.sh -y --url http://example.invalid/zoom-checker"
check "--url over plain http refused" test "$RC" -ne 0
check "  ... with a Russian message" has "https://"
check "install.sh refuses http before curl even runs" hasnt "скачиваю"
run "/root/bundle/install.sh --sha256 xyz"
check "malformed --sha256 refused" test "$RC" -ne 0

echo "== switch to TLS auto: a port the administrator opened stays theirs"
x ufw allow 443/tcp >/dev/null
run "cd /root/bundle && ./install.sh -y --tls auto --public-url https://zc.example.test"
check "auto-mode install succeeds" test "$RC" -eq 0
check "port 80 opened and recorded, 443 (administrator's) not recorded" \
  test "$(fwstate | LC_ALL=C sort | tr '\n' ' ')" = "ufw 80 ufw 8080 "
check "Let's Encrypt is not mentioned in English" hasnt "cannot issue"

echo "== uninstall closes what the installer opened"
x bash -c 'echo "ufw 9999" >>/etc/zoom-checker/firewall-ports && ufw allow 9999/tcp >/dev/null'
x bash -c 'nohup python3 -m http.server 9999 >/dev/null 2>&1 & echo $! >/root/listener.pid'; sleep 1
run "/root/bundle/install.sh --uninstall"
check "uninstall succeeds" test "$RC" -eq 0
added=$(ufw_added)
check "ports 80 and 8080 closed" bash -c '! grep -qE "^ufw allow (80|8080)/tcp$" <<<"$1"' _ "$added"
check "administrator's 22 and 443 kept" bash -c 'grep -qx "ufw allow 22/tcp" <<<"$1" && grep -qx "ufw allow 443/tcp" <<<"$1"' _ "$added"
check "a recorded port another program listens on is kept" grep -qx 'ufw allow 9999/tcp' <<<"$added"
check "  ... and the administrator is told why and how to close it" has "sudo ufw delete allow 9999/tcp"
check "443 is reported with the exact command to close it" has "sudo ufw delete allow 443/tcp"
check "the record is gone" test -z "$(fwstate)"
check "settings kept without --purge" x test -f /etc/zoom-checker/zoom-checker.env
x bash -c 'kill "$(cat /root/listener.pid)"; ufw --force delete allow 9999/tcp >/dev/null; ufw --force delete allow 443/tcp >/dev/null; ufw --force disable >/dev/null'

echo "== firewalld (stand-in) and --purge"
xi bash -c 'mkdir -p /var/lib/fakefw && cat >/usr/local/bin/firewall-cmd && chmod 755 /usr/local/bin/firewall-cmd' <<'EOF'
#!/bin/bash
# Stand-in for firewall-cmd with the exit codes of the real one.
echo "$*" >>/var/log/fake-firewall-cmd.log
systemctl is-active --quiet firewalld || { echo "FirewallD is not running" >&2; exit 252; }
perm=0 act="" arg=""
for a in "$@"; do
  case "$a" in
    --permanent) perm=1 ;; --quiet) ;;
    --reload) cp /var/lib/fakefw/permanent /var/lib/fakefw/runtime 2>/dev/null; exit 0 ;;
    --query-port=* | --add-port=* | --remove-port=*) act=${a%%=*}; arg=${a#*=} ;;
    *) echo "unsupported: $a" >&2; exit 2 ;;
  esac
done
f=/var/lib/fakefw/runtime; [ $perm -eq 1 ] && f=/var/lib/fakefw/permanent; touch "$f"
case "$act" in
  --query-port) grep -qxF "$arg" "$f" && { echo yes; exit 0; }; echo no; exit 1 ;;
  --add-port) grep -qxF "$arg" "$f" || echo "$arg" >>"$f"; exit 0 ;;
  --remove-port) grep -vxF "$arg" "$f" >"$f.new" || true; mv "$f.new" "$f"; exit 0 ;;
esac
EOF
x bash -c 'printf "[Service]\nExecStart=/bin/sleep infinity\n" >/etc/systemd/system/firewalld.service && systemctl daemon-reload && systemctl start firewalld'
run "cd /root/bundle && ./install.sh -y --tls off --port 8081 --public-url http://10.0.0.5:8081"
check "install with firewalld succeeds" test "$RC" -eq 0
check "firewalld: permanent and runtime rule for 8081" \
  x bash -c 'grep -qx 8081/tcp /var/lib/fakefw/permanent && grep -qx 8081/tcp /var/lib/fakefw/runtime'
check "firewalld: no --reload (keeps the administrator's runtime rules)" x bash -c '! grep -q -- --reload /var/log/fake-firewall-cmd.log'
check "recorded as firewalld 8081" test "$(fwstate)" = "firewalld 8081"
run "/root/bundle/install.sh --uninstall --purge"
check "uninstall --purge succeeds" test "$RC" -eq 0
check "firewalld: 8081 closed (permanent and runtime)" \
  x bash -c '! grep -qx 8081/tcp /var/lib/fakefw/permanent && ! grep -qx 8081/tcp /var/lib/fakefw/runtime'
check "settings, data and user removed" x bash -c '! test -e /etc/zoom-checker && ! test -e /var/lib/zoom-checker && ! id zoom-checker 2>/dev/null'
x bash -c 'systemctl stop firewalld; rm -f /etc/systemd/system/firewalld.service /usr/local/bin/firewall-cmd; systemctl daemon-reload'

echo "== interactive: hidden Secret Token, time zone question on a UTC server"
x timedatectl set-timezone Etc/UTC
# A pty driver that answers each prompt only after it appears, as a person would.
xi bash -c 'cat >/root/drive.py' <<'EOF'
import os, pty, select, sys, time
answers = [("Адрес сервиса", "http://10.0.0.5:8080"),
           ("Secret Token", "  tok_Hidden_321  "),
           ("Часовой пояс", "Mars/Base"),
           ("Часовой пояс", "Asia/Novosibirsk")]
pid, fd = pty.fork()
if pid == 0:
    os.chdir("/root/bundle")
    os.execv("./install.sh", ["./install.sh", "--tls", "off"])
out, pos, deadline = b"", 0, time.time() + 120
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 0.2)
    if r:
        try:
            data = os.read(fd, 4096)
        except OSError:
            break
        if not data:
            break
        out += data
    if answers:
        at = out.find(answers[0][0].encode(), pos)
        if at >= 0 and out.endswith((b": ", b"]: ")):
            pos = len(out)
            os.write(fd, (answers.pop(0)[1] + "\n").encode())
_, status = os.waitpid(pid, 0)
sys.stdout.write(out.decode("utf-8", "replace"))
sys.exit(os.waitstatus_to_exitcode(status))
EOF
run "python3 /root/drive.py"
check "interactive install succeeds" test "$RC" -eq 0
check "the Secret Token is never shown on screen" hasnt "tok_Hidden_321"
check "the Secret Token is saved, trimmed" test "$(envv ZOOM_WEBHOOK_SECRET_TOKEN)" = tok_Hidden_321
check "  ... and its length is confirmed" has "Secret Token сохранён (длина: 14)"
check "UTC server: Moscow offered as the default" has "[Europe/Moscow]"
check "an unknown time zone is asked again" has "неизвестный часовой пояс «Mars/Base»"
check "the chosen time zone is saved" test "$(envv TIMEZONE)" = Asia/Novosibirsk
check "prompts are in Russian" has "Адрес сервиса в интернете"
check "first-login password line in Russian" has "Первый вход:"
check "no host firewall: the cloud security group is mentioned" has "группе безопасности облака"

echo "== uninstall without a host firewall"
run "/root/bundle/install.sh --uninstall --purge"
check "uninstall --purge succeeds" test "$RC" -eq 0
check "names the port to close in the cloud / external firewall" has "порты 8080 (TCP)"

echo
echo "passed: $PASS, failed: $FAIL"
[ "$FAIL" -eq 0 ]
