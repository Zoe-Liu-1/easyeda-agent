package app

import (
	"fmt"
	"sort"
	"strings"
)

// Inspect every canvas frame in the public module-frame style, including stale
// frames absent from the current plan/ownership ledger. Do not count titles as
// proof of rectangle geometry.
func schFrameCollisionFindings(s schFrameSurvey) []checkFinding {
	ids := make([]string, 0, len(s.Rectangles))
	for id := range s.Rectangles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var frames []schFrameSpec
	var out []checkFinding
	for _, id := range ids {
		m := s.Rectangles[id]
		color, _ := m["color"].(string)
		if !strings.EqualFold(color, "#AA00AA") {
			continue
		}
		x, xok := m["x"].(float64)
		y, yok := m["y"].(float64)
		w, wok := m["width"].(float64)
		h, hok := m["height"].(float64)
		rot, rok := m["rotation"].(float64)
		if !xok || !yok || !wok || !hok || !rok || !plFinite(x) || !plFinite(y) || !plFinite(w) || !plFinite(h) || w <= 0 || h <= 0 || rot != 0 {
			out = append(out, checkFinding{Type: "partition-geometry-unavailable", Level: "ERROR", PrimitiveId: id, Message: "module frame geometry missing/unsupported; layout acceptance is unverified"})
			continue
		}
		frames = append(frames, schFrameSpec{ID: id, Rect: layoutBBox{MinX: x, MaxX: x + w, MinY: y - h, MaxY: y}})
	}
	for i, a := range frames {
		for _, b := range frames[i+1:] {
			if a.Rect.MinX < b.Rect.MaxX && a.Rect.MaxX > b.Rect.MinX && a.Rect.MinY < b.Rect.MaxY && a.Rect.MaxY > b.Rect.MinY {
				out = append(out, checkFinding{Type: "partition-overlap", Level: "ERROR", PrimitiveIds: []string{a.ID, b.ID}, Message: fmt.Sprintf("live module frames %s and %s overlap (including containment/duplicate frames)", a.ID, b.ID)})
			}
		}
	}
	return out
}

func fetchSchVisualSurvey(cfg *appConfig, window string, comps []layoutComp) (map[string]any, error) {
	pinned, win, doc, err := pinZonePage(cfg, window)
	if err != nil {
		return nil, err
	}
	value, err := execAutolayoutZoneJS(pinned, win, doc, "visual-survey", buildSchVisualSurveyJS(comps))
	if err != nil {
		return nil, err
	}
	return value, nil
}

func liveSchFrameCollisionFindings(cfg *appConfig, window string, comps []layoutComp, wires []schGroupWire) []checkFinding {
	value, err := fetchSchVisualSurvey(cfg, window, comps)
	if err != nil {
		return []checkFinding{{Type: "partition-geometry-unavailable", Level: "ERROR", Message: fmt.Sprintf("live visual check could not run: %v", err)}}
	}
	return schVisualSurveyFindings(value, comps, wires)
}

func schVisualSurveyFindings(value map[string]any, comps []layoutComp, wires []schGroupWire) []checkFinding {
	s, err := parseSchFrameSurvey(value)
	if err != nil {
		return []checkFinding{{Type: "partition-geometry-unavailable", Level: "ERROR", Message: err.Error()}}
	}
	out := append(schFrameCollisionFindings(s), schSurveyTextFindings(s, comps)...)
	ds, err := parseSchDesignatorGeometry(value)
	if err != nil {
		return append(out, checkFinding{Type: "designator-geometry-unavailable", Level: "ERROR", Message: err.Error()})
	}
	return append(out, schDesignatorFindings(s, ds, comps, wires)...)
}
