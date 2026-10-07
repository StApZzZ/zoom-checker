# Corporate login e2e (Keycloak, OpenLDAP, Samba AD)

Playwright project `auth`: real logins against the identity-provider stand in
`deploy/dev/auth-stand` (accounts and ports in its README). Skipped unless
`E2E_AUTH_STAND` is set, so the plain `npm run e2e` does not need the stand.

```bash
make build                                   # bin/zoom-checker with the current web/dist
deploy/dev/auth-stand/stand.sh up            # Keycloak, OpenLDAP, Samba AD (privileged)
cd web
E2E_AUTH_STAND=1 npm run e2e -- --project=auth
cd .. && deploy/dev/auth-stand/stand.sh down
```

Each spec file starts its own instance (SQLite, fresh data directory) on
`127.0.0.1:8080` with `PUBLIC_URL=http://localhost:8080`, because the realm `edu`
accepts only that redirect URI. Port 8080 must be free, and the project runs one
test at a time (`workers: 1`). Every test uses fresh browser contexts, so neither
the app's nor Keycloak's session carries over between tests. The instance's log
is copied to `test-results/auth-<directory>-server.log`.

| Spec | Instance | What |
|---|---|---|
| `keycloak-openldap.spec.ts` | OIDC + OpenLDAP (`ldap://`, preset `openldap`) + local admin | Keycloak teacher lands on the requested page as a teacher, no admin pages; Keycloak admin gets them; student refused with the required roles, «Войти под другой учётной записью» ends the student's Keycloak session and Keycloak shows an empty login form; app logout ends the Keycloak session; OpenLDAP teacher1 login; wrong password / unknown login → «Неверный логин или пароль»; student1 refused with the groups to join; local admin signs in alongside; `/admin/users` lists the accounts with source LDAP / Keycloak; blocking LDAP/Keycloak accounts signs them out and refuses them until enabled again (the blocked Keycloak account can switch to another one) |
| `active-directory.spec.ts` | OIDC + Samba AD (`ldaps://`, preset `ad`, `LDAP_INSECURE_SKIP_VERIFY`) + local admin | `CORP\t.ivanova` and `t.ivanova@corp.example.test` (teacher only through a nested group) sign in to one account; disabled `carol` gets the generic message; `s.sidorov` is refused with the groups to join; AD admin `a.petrov`, Keycloak and the local admin side by side in `/admin/users` |

OpenLDAP locks an account for 5 minutes after 3 wrong passwords in a row; the
specs use one wrong password per run and then a successful login, which resets
the count.
