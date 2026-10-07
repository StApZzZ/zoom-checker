package auth

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"zoom-checker/internal/model"
)

func TestMapRole(t *testing.T) {
	t.Parallel()
	const (
		admin   = model.RoleAdmin
		teacher = model.RoleTeacher
		none    = model.Role("")
	)
	adDN := "CN=ZoomChecker-Teachers,OU=ZoomGroups,DC=corp,DC=example,DC=test"
	cases := []struct {
		name            string
		values          []string
		admins, teacher []string
		want            model.Role
	}{
		// OIDC roles and groups
		{"realm role", []string{"offline_access", "zoom-checker-teacher"}, []string{"zoom-checker-admin"}, []string{"zoom-checker-teacher"}, teacher},
		{"case-insensitive", []string{"Zoom-Checker-ADMIN"}, []string{"zoom-checker-admin"}, nil, admin},
		{"group with slash", []string{"/teachers"}, nil, []string{"teachers"}, teacher},
		{"rule with slash", []string{"teachers"}, nil, []string{"/teachers"}, teacher},
		{"nested group last segment", []string{"/staff/zoom-teachers"}, nil, []string{"zoom-teachers"}, teacher},
		{"full path rule", []string{"/staff/zoom-teachers"}, nil, []string{"/staff/zoom-teachers"}, teacher},
		{"full path rule without slash", []string{"/staff/zoom-teachers"}, nil, []string{"staff/zoom-teachers"}, teacher},
		{"full path rule other parent", []string{"/other/zoom-teachers"}, nil, []string{"/staff/zoom-teachers"}, none},
		{"middle segment does not count", []string{"/zoom-teachers/sub"}, nil, []string{"zoom-teachers"}, none},
		{"prefix does not count", []string{"zoom-teachers-old"}, nil, []string{"zoom-teachers"}, none},
		{"admin wins", []string{"t", "a"}, []string{"a"}, []string{"t"}, admin},
		{"star teacher", nil, nil, []string{"*"}, teacher},
		{"star teacher, admin match", []string{"a"}, []string{"a"}, []string{"*"}, admin},
		{"star is not an admin wildcard", []string{"x"}, []string{"*"}, nil, none},
		{"no rules", []string{"zoom-checker-teacher"}, nil, nil, none},
		{"no values", nil, []string{"a"}, []string{"t"}, none},
		{"blank rule ignored", []string{""}, []string{" "}, []string{""}, none},
		{"rule whitespace trimmed", []string{"teachers"}, nil, []string{"  teachers "}, teacher},
		// LDAP DNs and CNs
		{"dn rule normalized", []string{adDN}, nil, []string{"cn=zoomchecker-teachers, ou=zoomgroups, dc=corp,dc=example,dc=test"}, teacher},
		{"dn rule spaces around =", []string{adDN}, nil, []string{"CN = ZoomChecker-Teachers , OU=ZoomGroups,DC=corp,DC=example,DC=test"}, teacher},
		{"dn rule other ou", []string{adDN}, nil, []string{"CN=ZoomChecker-Teachers,OU=Other,DC=corp,DC=example,DC=test"}, none},
		{"dn rule vs plain value", []string{"ZoomChecker-Teachers"}, nil, []string{"cn=zoomchecker-teachers,dc=x"}, none},
		{"cn rule vs dn value", []string{"CN=ZoomChecker-Admins,OU=G,DC=x"}, []string{"zoomchecker-admins"}, nil, admin},
		{"cn rule does not match parent ou", []string{"CN=Other,OU=zoomchecker-admins,DC=x"}, []string{"zoomchecker-admins"}, nil, none},
		{"escaped comma in cn", []string{`CN=Doe\, John,OU=G,DC=x`}, nil, []string{`cn=doe\, john,ou=g,dc=x`}, teacher},
		{"cn rule with comma", []string{`CN=Doe\, John,OU=G,DC=x`}, nil, []string{"doe, john"}, teacher},
		{"cyrillic cn", []string{"CN=Преподаватели,OU=Группы,DC=x"}, nil, []string{"преподаватели"}, teacher},
		{"cn rule with leading slash", []string{"CN=Teachers,OU=G,DC=x"}, nil, []string{"/teachers"}, teacher},
		// Over-broad matches that must not grant anything.
		{"admin is not admins or superadmin", []string{"/admins", "superadmin", "/x/admins", "admin-old", "admin/x"}, []string{"admin"}, nil, none},
		{"dn cn is not a path (slash inside cn)", []string{"CN=Teachers/ZoomChecker-Admins,OU=Delegated,DC=corp"}, []string{"ZoomChecker-Admins"}, nil, none},
		{"dn cn is not a path (leading slash)", []string{"CN=/ZoomChecker-Admins,OU=Delegated,DC=corp"}, []string{"ZoomChecker-Admins"}, nil, none},
		{"dn tail is not a path", []string{"CN=x,OU=a/ZoomChecker-Admins"}, []string{"ZoomChecker-Admins"}, nil, none},
		{"slash-only rule", []string{"/", "/staff/", "x/"}, []string{"/"}, []string{" / "}, none},
		{"dn rule vs oidc path with =", []string{"/cn=admins"}, []string{"cn=admins"}, nil, none},
		{"rule case and unicode", []string{"/STAFF/Учителя"}, nil, []string{"учителя"}, teacher},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, ok := MapRole(c.values, c.admins, c.teacher)
			if got != c.want || ok != (c.want != none) {
				t.Fatalf("MapRole(%q, %q, %q) = %q, %v; want %q", c.values, c.admins, c.teacher, got, ok, c.want)
			}
		})
	}
}

func TestNormalizeDN(t *testing.T) {
	t.Parallel()
	same := [][2]string{
		{"CN=A,OU=B,DC=c", "cn=a,ou=b,dc=c"},
		{"cn = A , ou = B", "CN=a,OU=b"},
		{`CN=Doe\, John,DC=x`, `cn=doe\2C john,dc=x`},
		{"CN=A+UID=1,DC=x", "cn=a+uid=1,dc=x"},
	}
	for _, p := range same {
		if a, b := normalizeDN(p[0]), normalizeDN(p[1]); a != b {
			t.Errorf("normalizeDN(%q)=%q != normalizeDN(%q)=%q", p[0], a, p[1], b)
		}
	}
	if normalizeDN("CN=A,DC=x") == normalizeDN("CN=A,DC=y") {
		t.Error("different DNs must differ")
	}
}

func TestAccessDeniedError(t *testing.T) {
	t.Parallel()
	var err error = &AccessDeniedError{Username: "ivanov", Required: []string{"teachers", "admins"}}
	wrapped := fmt.Errorf("login: %w", err)
	if !errors.Is(wrapped, ErrAccessDenied) {
		t.Fatal("errors.Is(AccessDeniedError, ErrAccessDenied) must be true")
	}
	var ad *AccessDeniedError
	if !errors.As(wrapped, &ad) || ad.Required[0] != "teachers" {
		t.Fatal("errors.As must find *AccessDeniedError")
	}
	if errors.Is(wrapped, ErrInvalidCredentials) {
		t.Fatal("access denied is not invalid credentials")
	}
	if msg := UserMessage(wrapped); !strings.Contains(msg, "teachers, admins") {
		t.Fatalf("UserMessage must name the required groups: %q", msg)
	}
}

func TestUserMessage(t *testing.T) {
	t.Parallel()
	cases := map[error]string{
		nil:                                  "",
		ErrInvalidCredentials:                "Неверный логин или пароль.",
		unavailable("x", errors.New("y")):    "Сервис входа сейчас недоступен",
		fmt.Errorf("%w: state", ErrOIDCFlow): "Не удалось завершить вход",
		&AccessDeniedError{}:                 "никому не разрешён",
		errors.New("other"):                  "Не удалось войти",
	}
	for err, want := range cases {
		if got := UserMessage(err); !strings.Contains(got, want) || (want == "" && got != "") {
			t.Errorf("UserMessage(%v) = %q, want it to contain %q", err, got, want)
		}
	}
}
