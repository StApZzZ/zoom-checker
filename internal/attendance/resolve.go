package attendance

import (
	"slices"

	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
)

// resolution is the verdict for one normalized display name.
type resolution struct {
	res        model.Resolution
	student    int64 // valid when hasStudent
	hasStudent bool
	reason     model.IgnoreReason
	candidates []int64
}

type resolver struct {
	byName     map[string][]int64 // normalized name/alias of active students -> student ids (ascending)
	roster     map[int64]bool     // every roster student id (manual mappings may target archived ones)
	mappings   map[string]Mapping
	ignored    map[string]bool
	ignoreHost bool
	hosts      map[string]bool // names used by a session host device
}

func newResolver(in *Input, hosts map[string]bool) *resolver {
	r := &resolver{
		byName:     make(map[string][]int64),
		roster:     make(map[int64]bool, len(in.Roster)),
		mappings:   normalizeMappings(in.Mappings),
		ignored:    make(map[string]bool, len(in.IgnoredNames)),
		ignoreHost: in.IgnoreHost,
		hosts:      hosts,
	}
	for _, s := range in.Roster {
		if r.roster[s.StudentID] {
			continue // duplicate roster row: the first one wins
		}
		r.roster[s.StudentID] = true
		if !s.Active {
			continue
		}
		r.addName(s.NormalizedName, s.StudentID)
		for _, a := range s.NormalizedAliases {
			r.addName(a, s.StudentID)
		}
	}
	for _, ids := range r.byName {
		slices.Sort(ids)
	}
	for n, on := range in.IgnoredNames {
		if on {
			r.ignored[names.Normalize(n)] = true
		}
	}
	return r
}

func (r *resolver) addName(raw string, id int64) {
	n := names.Normalize(raw) // idempotent; guards against callers passing raw names
	if n == "" {
		return // an empty name never matches
	}
	if ids := r.byName[n]; !slices.Contains(ids, id) {
		r.byName[n] = append(ids, id)
	}
}

// normalizeMappings re-keys mappings by normalized name. Keys that already are
// normalized win; colliding raw keys are applied in sorted order so the result is
// deterministic.
func normalizeMappings(in map[string]Mapping) map[string]Mapping {
	out := make(map[string]Mapping, len(in))
	var raw []string
	for k, m := range in {
		if names.Normalize(k) == k {
			out[k] = m
		} else {
			raw = append(raw, k)
		}
	}
	slices.Sort(raw)
	for _, k := range raw {
		n := names.Normalize(k)
		if _, taken := out[n]; !taken {
			out[n] = in[k]
		}
	}
	return out
}

// resolve applies rule 2 to one normalized display name. The teacher's mapping always
// wins; the ignore list and the host rule only apply to names that match nobody in the
// roster, so a real student is never hidden by a global setting (TZ §32).
func (r *resolver) resolve(name string) resolution {
	if mp, ok := r.mappings[name]; ok {
		switch mp.Action {
		case model.MappingStudent:
			if r.roster[mp.StudentID] {
				return resolution{res: model.ResolutionManual, student: mp.StudentID, hasStudent: true}
			}
			// The target is not part of this lesson's roster (deleted / other group).
			return resolution{res: model.ResolutionUnidentified}
		case model.MappingIgnore:
			return resolution{res: model.ResolutionIgnored, reason: model.IgnoreManual}
		case model.MappingNone:
			return resolution{res: model.ResolutionUnidentified}
		}
		// Unknown action: fall through to the automatic rules.
	}
	if name == "" {
		return resolution{res: model.ResolutionUnidentified}
	}
	switch ids := r.byName[name]; len(ids) {
	case 0:
	case 1:
		return resolution{res: model.ResolutionMatched, student: ids[0], hasStudent: true}
	default:
		return resolution{res: model.ResolutionConflict, candidates: slices.Clone(ids)}
	}
	if r.ignored[name] {
		return resolution{res: model.ResolutionIgnored, reason: model.IgnoreList}
	}
	if r.ignoreHost && r.hosts[name] {
		return resolution{res: model.ResolutionIgnored, reason: model.IgnoreHost}
	}
	return resolution{res: model.ResolutionUnidentified}
}
