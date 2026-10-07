package auth

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// DiagStep is one step of a connectivity / configuration check, shown by the
// check-ldap / check-oidc CLI commands and the admin UI. Name and Detail are
// Russian and never contain secrets.
type DiagStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// diagSteps collects steps; add returns ok so callers can stop at the first failure.
type diagSteps []DiagStep

func (d *diagSteps) add(name string, ok bool, detail string) bool {
	*d = append(*d, DiagStep{Name: name, OK: ok, Detail: detail})
	return ok
}

// describeNetError explains a connection error in plain Russian.
func describeNetError(err error, timeout time.Duration) string {
	var (
		unknownCA x509.UnknownAuthorityError
		hostname  x509.HostnameError
		invalid   x509.CertificateInvalidError
		record    tls.RecordHeaderError
		dnsErr    *net.DNSError
		netErr    net.Error
	)
	switch {
	case errors.As(err, &unknownCA):
		return "Сертификат сервера выдан неизвестным центром сертификации. Укажите файл корневого сертификата (CA) в настройках или проверьте, что указан правильный файл."
	case errors.As(err, &hostname):
		return fmt.Sprintf("Имя сервера в адресе не совпадает с его сертификатом (%v). Укажите в адресе то имя, на которое выдан сертификат.", hostname.Error())
	case errors.As(err, &invalid):
		return "Сертификат сервера недействителен (истёк, ещё не начал действовать или не подходит для этой цели)."
	case errors.As(err, &record):
		return "Сервер ответил без шифрования TLS. Для ldaps:// обычно нужен порт 636, для ldap:// — порт 389."
	case errors.As(err, &dnsErr):
		return fmt.Sprintf("Не удалось найти сервер по имени %q. Проверьте адрес и настройки DNS.", dnsErr.Name)
	case errors.Is(err, syscall.ECONNREFUSED):
		return "Соединение отклонено: на этом адресе и порту сервер не принимает подключения."
	case errors.As(err, &netErr) && netErr.Timeout():
		return fmt.Sprintf("Сервер не ответил за %s. Проверьте адрес, порт и доступ по сети (брандмауэр).", timeout)
	case ldap.IsErrorWithCode(err, ldap.LDAPResultProtocolError):
		return "Сервер не поддерживает StartTLS. Используйте ldaps:// или настройте TLS на сервере."
	}
	// go-ldap flattens StartTLS handshake errors into text.
	switch msg := err.Error(); {
	case strings.Contains(msg, "certificate signed by unknown authority"):
		return describeNetError(x509.UnknownAuthorityError{}, timeout)
	case strings.Contains(msg, "certificate is valid for"), strings.Contains(msg, "certificate is not valid for any names"):
		return "Имя сервера в адресе не совпадает с его сертификатом. Укажите в адресе то имя, на которое выдан сертификат."
	case strings.Contains(msg, "certificate has expired"), strings.Contains(msg, "not yet valid"):
		return "Сертификат сервера недействителен (истёк или ещё не начал действовать)."
	}
	return "Не удалось подключиться: " + err.Error()
}
