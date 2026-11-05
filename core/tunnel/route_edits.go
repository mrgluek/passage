package tunnel

import (
	"slices"

	links "github.com/mrgluek/passage-link/link"
)

// RouteEdit is a local change to one installed route, kept apart from the
// signed link: a different priority, or the route switched off.
type RouteEdit struct {
	Priority *uint16 `json:"priority,omitempty"`
	Off      bool    `json:"off,omitempty"`
}

// Edited is the routes with the local edits applied, in the order the
// router tries them. A route that is off is left out.
func Edited(routes []links.Route, edits map[string]RouteEdit) []links.Route {
	out := make([]links.Route, 0, len(routes))
	for _, r := range routes {
		e := edits[r.ID]
		if e.Off {
			continue
		}
		if e.Priority != nil {
			r.Priority = *e.Priority
		}
		out = append(out, r)
	}
	return links.Sorted(out)
}

// MinimalEdits keeps only what differs from the link: the priority when it
// is not the link's own, and the off flag. Edits for routes that are not
// installed are dropped.
func MinimalEdits(routes []links.Route, edits map[string]RouteEdit) map[string]RouteEdit {
	out := map[string]RouteEdit{}
	for id, e := range edits {
		i := slices.IndexFunc(routes, func(r links.Route) bool { return r.ID == id })
		if i < 0 {
			continue
		}
		if e.Priority != nil && *e.Priority == routes[i].Priority {
			e.Priority = nil
		}
		if e.Priority != nil || e.Off {
			out[id] = e
		}
	}
	return out
}
