#!/bin/sh
# Runs inside zc-test-samba (docker exec) after sambacc created the users.
# sambacc cannot nest groups or disable users, so do it here. Idempotent.
set -eu
i=0
until samba-tool user show svc-zoom >/dev/null 2>&1; do
  i=$((i + 1))
  [ "$i" -gt 90 ] && { echo "samba users were not created in time" >&2; exit 1; }
  sleep 2
done
# t.ivanova is a teacher only through nesting: helpdesk ∈ ZoomChecker-Teachers.
if ! samba-tool group listmembers ZoomChecker-Teachers | grep -qx helpdesk; then
  samba-tool group addmembers ZoomChecker-Teachers helpdesk
fi
# carol stays in ZoomChecker-Teachers but cannot log in (AD error 533).
samba-tool user disable carol >/dev/null
# sambacc sets no e-mail; give t.ivanova one to check the mail mapping.
ldbmodify -H /var/lib/samba/private/sam.ldb >/dev/null <<LDIF
dn: CN=Татьяна Иванова,OU=ZoomUsers,DC=corp,DC=example,DC=test
changetype: modify
replace: mail
mail: t.ivanova@corp.example.test
LDIF
echo "samba init done"
