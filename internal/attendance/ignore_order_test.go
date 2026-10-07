package attendance

import (
	"testing"

	"zoom-checker/internal/model"
)

// TestIgnoreListNeverHidesGroupNames is the regression test for the rule order
// of §5.4 rule 2 (teacher mapping → empty name → names and aliases of the
// lesson's group → ignore list → host): a name on the global ignore list that
// equals a student's name or alias in this lesson's group is matched to the
// student, whatever group the name was hidden for. Only names nobody in the
// group uses are hidden by the list.
func TestIgnoreListNeverHidesGroupNames(t *testing.T) {
	t.Parallel()
	withAlias := func() []RosterStudent {
		r := roster()
		r[3].NormalizedAliases = []string{"александр", "саша"} // Александров Александр
		return r
	}
	cases := []struct {
		name      string
		roster    []RosterStudent
		ignored   map[string]bool
		mappings  map[string]Mapping
		zoomName  string
		wantNorm  string
		want      model.Resolution
		wantID    int64 // for matched / manual
		wantWhy   model.IgnoreReason
		wantFound bool // the student is credited
	}{
		{"full name on the list", roster(), map[string]bool{"Иванов Иван": true}, nil,
			"ИВАНОВ  Иван", "иванов иван", model.ResolutionMatched, ivanov, model.IgnoreNone, true},
		{"alias on the list (hidden in another group)", withAlias(), map[string]bool{"Александр": true}, nil,
			"Александр", "александр", model.ResolutionMatched, alexandrov, model.IgnoreNone, true},
		{"list entry spelled differently", withAlias(), map[string]bool{"  САША ": true}, nil,
			"саша", "саша", model.ResolutionMatched, alexandrov, model.IgnoreNone, true},
		{"same name in a group without that alias", roster(), map[string]bool{"Александр": true}, nil,
			"Александр", "александр", model.ResolutionIgnored, 0, model.IgnoreList, false},
		{"archived student's alias does not count", func() []RosterStudent {
			r := withAlias()
			r[3].Active = false
			return r
		}(), map[string]bool{"Александр": true}, nil,
			"Александр", "александр", model.ResolutionIgnored, 0, model.IgnoreList, false},
		{"teacher's decision still wins", withAlias(), map[string]bool{"Александр": true},
			map[string]Mapping{"александр": {Action: model.MappingIgnore}},
			"Александр", "александр", model.ResolutionIgnored, 0, model.IgnoreManual, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			inp := baseInput(join("a", c.zoomName, hm(18, 0)))
			inp.Roster = c.roster
			inp.IgnoredNames = c.ignored
			inp.Mappings = c.mappings
			out := Compute(inp)
			p := participant(t, out, c.wantNorm)
			if p.Resolution != c.want || p.IgnoreReason != c.wantWhy {
				t.Fatalf("resolution %q/%q, want %q/%q", p.Resolution, p.IgnoreReason, c.want, c.wantWhy)
			}
			if c.wantID != 0 && (p.StudentID == nil || *p.StudentID != c.wantID) {
				t.Fatalf("student %v, want %d", p.StudentID, c.wantID)
			}
			if c.wantFound {
				if s := student(t, out, c.wantID); !s.Seen || s.AutoStatus != model.FinalPresent {
					t.Fatalf("student result %+v", s)
				}
			}
		})
	}
}
