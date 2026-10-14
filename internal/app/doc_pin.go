package app

import (
	"encoding/json"
	"sync"
)

// Per-process memory of the page the --doc guard last confirmed live.
//
// Audit 2026-10-13: the guard probed document.current before EVERY action, so
// 40% of a `pcb check` (35 of 87 calls) and 48% of a `sch gate` were probes —
// latency plus exposure to transient failures for no new information, since
// every response already carries the document it ran on. After one live
// confirmation, plain reads go straight out and are verified against their own
// response context (postAction re-pins and resends once on drift).
//
// Writes always keep the live pre-probe: a write on the wrong page cannot be
// undone after the fact. debug.exec_js keeps it too — its context is known to
// lag a page switch (it can report the old page), so its context is no proof.
// docGuardCache holds, per process (hung off appConfig): pins — the document
// UUID last confirmed for each window+--doc; names — what a --doc NAME
// resolved to, so the guard's fast path matches it without re-listing every
// document (a name used to cost document.current + pages.list +
// pcb.documents.list on every call).
type docGuardCache struct {
	pins, names sync.Map
}

var docGuardInit sync.Mutex

func (c *appConfig) docGuard() *docGuardCache {
	docGuardInit.Lock()
	defer docGuardInit.Unlock()
	if c.docCache == nil {
		c.docCache = &docGuardCache{}
	}
	return c.docCache
}

func docPinKey(cfg *appConfig, window string) string { return window + "\x00" + cfg.doc }

func docPinFor(cfg *appConfig, window, action string) (string, bool) {
	if actionMutates(action) || action == "debug.exec_js" {
		return "", false
	}
	v, ok := cfg.docGuard().pins.Load(docPinKey(cfg, window))
	if !ok {
		return "", false
	}
	return v.(string), true
}

func rememberDocPin(cfg *appConfig, window, uuid string) {
	if uuid != "" {
		cfg.docGuard().pins.Store(docPinKey(cfg, window), uuid)
	}
}

func forgetDocPin(cfg *appConfig, window string) { cfg.docGuard().pins.Delete(docPinKey(cfg, window)) }

// responseDocumentUUID is the document the connector reports the action ran
// on ("" when the response carries no context — treated as drift).
func responseDocumentUUID(body []byte) string {
	var r struct {
		Context *struct {
			DocumentUUID string `json:"documentUuid"`
		} `json:"context"`
	}
	if json.Unmarshal(body, &r) != nil || r.Context == nil {
		return ""
	}
	return r.Context.DocumentUUID
}
