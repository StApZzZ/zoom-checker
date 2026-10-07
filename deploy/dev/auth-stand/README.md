# Auth test stand

Keycloak, OpenLDAP and Samba AD in Docker for the `internal/auth` integration
tests and for trying LDAP / Keycloak login locally.

```bash
deploy/dev/auth-stand/stand.sh up            # all three; or: up keycloak openldap
eval "$(deploy/dev/auth-stand/stand.sh env)" # TEST_* variables
go test ./internal/auth/... -run Integration -v
deploy/dev/auth-stand/stand.sh down          # remove containers and .state/
```

All ports are published on `127.0.0.1` only: the stand uses well-known admin
passwords and a privileged Samba DC, never expose it to a network.

OpenLDAP's default password policy locks an account for 5 minutes after 3
consecutive wrong passwords (`pwdMaxFailure: 3`; a successful login resets the
count). If a test user is locked, wait or run `down` + `up`.

Samba needs a privileged container. `up` waits for health, then runs
`samba/init.sh` (nested group, disabled user, e-mail) and copies Samba's CA to
`.state/samba-ca.pem`. The Samba certificate is issued to
`dc1.corp.example.test`, so connecting via `localhost` needs
`LDAP_INSECURE_SKIP_VERIFY=true` (testing only).

| Service | Host port | Notes |
|---|---|---|
| Keycloak 26.8.0 | 18080 (HTTP), 19000 (health) | admin console: admin / admin; realm `edu` |
| OpenLDAP 2.6.10 | 13890 | base `dc=example,dc=test`, root `uid=admin,dc=example,dc=test` / `Root-Passw0rd!` |
| Samba AD 4.24 | 13636 (LDAPS), 13389 (LDAP + StartTLS) | realm `CORP.EXAMPLE.TEST`, `Administrator` / `Adm1n-Passw0rd!` |

## Accounts

**Keycloak** (realm `edu`, confidential client `zoom-checker`, secret
`$ZC_OIDC_CLIENT_SECRET`, default `zc-dev-oidc-secret`; redirect
`http://localhost:8080/auth/oidc/callback`). Realm roles reach the access
token only (Keycloak default); a `groups` mapper (full path) adds groups to
the ID token.

| User | Password | Gets |
|---|---|---|
| kc-admin | KcAdmin-Passw0rd! | realm role `zoom-checker-admin` |
| kc-teacher | KcTeacher-Passw0rd! | group `/staff/zoom-teachers` → role `zoom-checker-teacher` |
| kc-student | KcStudent-Passw0rd! | nothing (access denied) |

**OpenLDAP** (service account `uid=zc-svc,ou=TechnicalAccounts,ou=Users,dc=example,dc=test` / `Svc-Passw0rd!`)

| User | Password | Groups (`groupOfNames`) |
|---|---|---|
| teacher1 | Teacher1-Passw0rd! | zoom-checker-teachers |
| admin1 | Admin1-Passw0rd! | zoom-checker-admins |
| student1 | Student1-Passw0rd! | none |

**Samba AD** (service account `svc-zoom@corp.example.test` / `Svc-Passw0rd!`)

| User | Password | Groups |
|---|---|---|
| t.ivanova | Ivanova-Passw0rd! | helpdesk, which is a member of ZoomChecker-Teachers (nested) |
| a.petrov | Petrov-Passw0rd! | ZoomChecker-Admins |
| s.sidorov | Sidorov-Passw0rd! | none |
| carol | Carol-Passw0rd! | ZoomChecker-Teachers, but the account is disabled |

App settings for local login against the stand:

```bash
OIDC_ISSUER_URL=http://localhost:18080/realms/edu OIDC_CLIENT_ID=zoom-checker \
OIDC_CLIENT_SECRET=zc-dev-oidc-secret OIDC_ADMIN_ROLES=zoom-checker-admin OIDC_TEACHER_ROLES=zoom-checker-teacher
# or
LDAP_URL=ldaps://localhost:13636 LDAP_INSECURE_SKIP_VERIFY=true LDAP_PRESET=ad LDAP_DOMAIN=corp.example.test \
LDAP_BASE_DN=DC=corp,DC=example,DC=test LDAP_BIND_DN=svc-zoom@corp.example.test LDAP_BIND_PASSWORD='Svc-Passw0rd!' \
LDAP_ADMIN_GROUPS='CN=ZoomChecker-Admins,OU=ZoomGroups,DC=corp,DC=example,DC=test' \
LDAP_TEACHER_GROUPS=ZoomChecker-Teachers
```

The admin group is given as a full DN on purpose: a bare CN matches a group of
that name in any OU of the domain (see `docs/admin-guide.md` §9). The stand's
groups live in `OU=ZoomGroups`; `check-ldap --user a.petrov` lists them.
