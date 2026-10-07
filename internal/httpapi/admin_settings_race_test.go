package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"zoom-checker/internal/config"
)

// Two admins saving different fields at the same time keep both changes
// (partial PUTs merge over the stored value inside one unit).
func TestSettingsParallelPartialUpdates(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	_, admin := e.adminCookie()

	bodies := []map[string]any{
		{"present_min_percent": 40},
		{"ignored_names": []string{"Zoom Recorder"}},
		{"link_window_hours": 12},
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		codes []int
	)
	for range 5 {
		for _, body := range bodies {
			wg.Go(func() {
				code := e.do("PUT", "/api/settings", body, admin).Code
				mu.Lock()
				codes = append(codes, code)
				mu.Unlock()
			})
		}
	}
	wg.Wait()
	for _, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("PUT /api/settings answered %v", codes)
		}
	}
	st, err := e.st.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.PresentMinPercent != 40 || st.LinkWindowHours != 12 || len(st.IgnoredNames) != 1 || st.IgnoredNames[0] != "Zoom Recorder" {
		t.Fatalf("settings after parallel saves = %+v", st)
	}
	// Validation still happens before anything is stored.
	azWantError(t, e.do("PUT", "/api/settings", map[string]any{"present_min_percent": 101, "link_window_hours": 2}, admin),
		http.StatusUnprocessableEntity, CodeValidation)
	if st2, _ := e.st.GetSettings(context.Background()); st2.LinkWindowHours != 12 {
		t.Fatalf("invalid request changed settings: %+v", st2)
	}
}

func TestTestLDAPMisconfigured(t *testing.T) {
	t.Parallel()
	// LDAP_URL is set, but the client could not be built (s.LDAP stays nil).
	e := newAZEnv(t, func(s *azSetup) { s.cfg.LDAP = config.LDAPConfig{URL: "ldaps://dc.invalid:636"} })
	_, admin := e.adminCookie()
	msg := azWantError(t, e.do("POST", "/api/system/test-ldap", map[string]string{}, admin), http.StatusUnprocessableEntity, CodeValidation)
	if !strings.Contains(msg, "с ошибкой") || !strings.Contains(msg, "журнале") {
		t.Fatalf("message %q", msg)
	}
}
