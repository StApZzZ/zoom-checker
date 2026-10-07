#!/usr/bin/env bash
# Auth test stand: Keycloak (OIDC), OpenLDAP and Samba AD in Docker.
#
#   ./stand.sh up [keycloak|openldap|samba ...]   start (default: all) and wait until ready
#   ./stand.sh env                                print TEST_* variables for the integration tests
#   ./stand.sh down                               remove containers and state
#
# Typical run from the repository root:
#   deploy/dev/auth-stand/stand.sh up
#   eval "$(deploy/dev/auth-stand/stand.sh env)"
#   go test ./internal/auth/... -run Integration -v
#   deploy/dev/auth-stand/stand.sh down
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STATE="$DIR/.state"
export ZC_OIDC_CLIENT_SECRET="${ZC_OIDC_CLIENT_SECRET:-zc-dev-oidc-secret}"
COMPOSE=(docker compose -f "$DIR/docker-compose.yml")
ALL=(keycloak openldap samba)

log() { printf '[auth-stand] %s\n' "$*" >&2; }

# wait_for <what> <timeout seconds> <command...>
wait_for() {
  local what=$1 timeout=$2 start=$SECONDS
  shift 2
  until "$@" >/dev/null 2>&1; do
    if (( SECONDS - start > timeout )); then
      log "$what is not ready after ${timeout}s"
      return 1
    fi
    sleep 2
  done
  log "$what is ready ($(( SECONDS - start ))s)"
}

keycloak_ready() { curl -fsS http://localhost:19000/health/ready | grep -q '"UP"'; }

openldap_ready() {
  docker exec zc-test-openldap ldapsearch -x -H ldap://127.0.0.1:389 \
    -D uid=zc-svc,ou=TechnicalAccounts,ou=Users,dc=example,dc=test -w 'Svc-Passw0rd!' \
    -b dc=example,dc=test '(uid=student1)' dn | grep -q '^dn:'
}

samba_ready() { docker exec zc-test-samba samba-tool user show svc-zoom; }

cmd_up() {
  local services=("$@")
  (( ${#services[@]} )) || services=("${ALL[@]}")
  mkdir -p "$STATE"
  log "starting: ${services[*]}"
  "${COMPOSE[@]}" up -d "${services[@]}"
  local s
  for s in "${services[@]}"; do
    case "$s" in
      keycloak) wait_for keycloak 300 keycloak_ready ;;
      openldap) wait_for openldap 120 openldap_ready ;;
      samba)
        if ! wait_for samba 300 samba_ready; then
          log "Samba AD did not start (it needs a privileged container); last log lines:"
          docker logs --tail 30 zc-test-samba >&2 || true
          return 1
        fi
        docker exec zc-test-samba sh /zc/init.sh >&2
        docker cp zc-test-samba:/var/lib/samba/private/tls/ca.pem "$STATE/samba-ca.pem" >/dev/null
        ;;
      *) log "unknown service $s"; return 1 ;;
    esac
  done
  log "up. Test variables: $0 env"
}

cmd_down() {
  "${COMPOSE[@]}" down -v --remove-orphans
  rm -rf "$STATE"
}

running() { [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = "true" ]; }

cmd_env() {
  if running zc-test-keycloak; then
    cat <<EOF
export TEST_OIDC_ISSUER='http://localhost:18080/realms/edu'
export TEST_OIDC_CLIENT_ID='zoom-checker'
export TEST_OIDC_CLIENT_SECRET='$ZC_OIDC_CLIENT_SECRET'
EOF
  fi
  if running zc-test-openldap; then
    cat <<'EOF'
export TEST_LDAP_OPENLDAP_URL='ldap://localhost:13890'
export TEST_LDAP_OPENLDAP_BIND_DN='uid=zc-svc,ou=TechnicalAccounts,ou=Users,dc=example,dc=test'
export TEST_LDAP_OPENLDAP_BIND_PASSWORD='Svc-Passw0rd!'
export TEST_LDAP_OPENLDAP_BASE_DN='dc=example,dc=test'
EOF
  fi
  if running zc-test-samba; then
    cat <<EOF
export TEST_LDAP_AD_URL='ldaps://localhost:13636'
export TEST_LDAP_AD_STARTTLS_URL='ldap://localhost:13389'
export TEST_LDAP_AD_BIND_DN='svc-zoom@corp.example.test'
export TEST_LDAP_AD_BIND_PASSWORD='Svc-Passw0rd!'
export TEST_LDAP_AD_BASE_DN='DC=corp,DC=example,DC=test'
export TEST_LDAP_AD_DOMAIN='corp.example.test'
export TEST_LDAP_AD_CA_FILE='$STATE/samba-ca.pem'
EOF
  fi
}

case "${1:-}" in
  up) shift; cmd_up "$@" ;;
  down) cmd_down ;;
  env) cmd_env ;;
  *) echo "usage: $0 up [keycloak|openldap|samba ...] | env | down" >&2; exit 2 ;;
esac
