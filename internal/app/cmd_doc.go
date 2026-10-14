package app

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// openableDoc is one document a window can switch to (a schematic page or a PCB),
// unified across the schematic.pages.list and pcb.documents.list actions.
type openableDoc struct {
	UUID   string `json:"uuid"`
	Type   string `json:"type"` // "schematic" | "pcb"
	Name   string `json:"name"`
	Parent string `json:"parent,omitempty"` // owning schematic/project uuid
	Active bool   `json:"active"`
}

// newDocCmd returns the "doc" subcommand group — the self-service discover +
// switch loop. `doc ls` enumerates every openable document in the targeted
// window and marks the active one; `doc switch` resolves a name or uuid and
// brings that document to the front. Both route by the shared --project/--window
// flags, so an agent can drive a window without knowing its windowId or port.
func newDocCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	var window string

	doc := &cobra.Command{
		Use:   "doc",
		Short: "Discover and open/switch EasyEDA documents (schematic pages / PCBs)",
		Long: "Discover every openable document in a window and open or switch between them.\n\n" +
			"  easyeda doc ls --project <name>                      list all schematic pages + PCBs, ★=active\n" +
			"  easyeda doc open <name|uuid> --project <name>        open a document (schematic page or PCB)\n" +
			"  easyeda doc switch <name|uuid> --project <name>      switch to a document (same as open)\n" +
			"  easyeda doc reload [name|uuid] --project <name>      save, close, and reopen a document\n" +
			"  easyeda doc ready [uuid] --project <name>            verify current readability without navigation\n\n" +
			"Context is read live (not the connect-time snapshot), so the active marker\nand `daemon health` reflect the real foreground document. If the project has\nno active editor tab, `doc ls` still uses project inventories and `doc open`\ncan recover by UUID, then confirms the new active document with a fresh read.",
	}
	doc.PersistentFlags().StringVar(&window, "window", "", "EasyEDA window ID (usually prefer --project)")

	var jsonOut bool

	lsCmd := &cobra.Command{
		Use:   "ls",
		Short: "List all openable documents in the window (★ = active)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			docs, active, _, err := discoverDocs(cfg, window)
			if err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(stdout, map[string]any{
					"activeUuid": active,
					"documents":  docs,
				})
			}
			printDocTable(stdout, docs)
			return nil
		},
	}
	lsCmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of a table")

	switchCmd := &cobra.Command{
		Use:   "switch <name|uuid>",
		Short: "Switch the foreground document by page name, PCB name, or uuid",
		Args:  cobra.ExactArgs(1),
		Example: "  easyeda doc switch P2 --project motobox2026\n" +
			"  easyeda doc switch ESP32-S3-V1_0_1 --project motobox2026\n" +
			"  easyeda doc switch 6b3a2f01-... --project motobox2026",
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			docs, _, win, err := discoverDocs(cfg, window)
			if err != nil {
				return err
			}
			match, err := resolveDoc(docs, target)
			if err != nil {
				return err
			}
			// Pin the open + readback to the SAME window discoverDocs resolved.
			if _, err := requestAction(cfg, "document.open", win,
				map[string]any{"uuid": match.UUID}); err != nil {
				return err
			}
			// Re-read live context to confirm the switch took effect.
			cur, err := requestAction(cfg, "document.current", win, nil)
			if err != nil {
				return err
			}
			if cur.Context == nil || cur.Context.DocumentUUID != match.UUID {
				return fmt.Errorf("document.open returned but active document is not %s; the page may still be loading", match.UUID)
			}
			// document.open returns as soon as the tab exists — BEFORE the page's
			// primitives/netlist finish loading. Wait for the page data to settle
			// so a read fired right after the switch doesn't sample a half-loaded
			// page (issue #67). The probe follows the document type — a PCB is
			// polled with pcb.components.list rather than skipped (issue #161).
			ready := waitDocSettleFor(cfg, win, match.Type)
			if !ready {
				return fmt.Errorf("document %s became active but its %s data did not settle; stop writes and retain the failure/context; the unavailable readback is not successful reload evidence", match.UUID, match.Type)
			}
			out := map[string]any{
				"switchedTo": match,
				"ready":      ready,
			}
			if cur.Context != nil {
				out["active"] = cur.Context
			}
			if jsonOut {
				return writeJSON(stdout, out)
			}
			fmt.Fprintf(stdout, "✓ switched to %s %q (%s)\n", match.Type, match.Name, match.UUID)
			return nil
		},
	}
	switchCmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of a line")

	// open is a semantically clearer alias for switch — "open a document" vs "switch to a document"
	openCmd := &cobra.Command{
		Use:   "open <name|uuid>",
		Short: "Open a document (schematic page or PCB) by name or uuid",
		Args:  cobra.ExactArgs(1),
		Example: "  easyeda doc open PCB1 --project ceshi\n" +
			"  easyeda doc open P1 --project ceshi\n" +
			"  easyeda doc open ESP32-mini-v2 --project hardware",
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			docs, _, win, err := discoverDocs(cfg, window)
			if err != nil {
				return err
			}
			match, err := resolveDoc(docs, target)
			if err != nil {
				return err
			}
			if _, err := requestAction(cfg, "document.open", win,
				map[string]any{"uuid": match.UUID}); err != nil {
				return err
			}
			cur, err := requestAction(cfg, "document.current", win, nil)
			if err != nil {
				return err
			}
			if cur.Context == nil || cur.Context.DocumentUUID != match.UUID {
				return fmt.Errorf("document.open returned but active document is not %s; the page may still be loading", match.UUID)
			}
			if !waitDocSettleFor(cfg, win, match.Type) {
				return fmt.Errorf("document %s became active but its %s data did not settle; stop writes and retain the failure/context; the unavailable readback is not successful reload evidence", match.UUID, match.Type)
			}
			out := map[string]any{
				"opened": match,
				"ready":  true,
			}
			if cur.Context != nil {
				out["active"] = cur.Context
			}
			if jsonOut {
				return writeJSON(stdout, out)
			}
			fmt.Fprintf(stdout, "✓ opened %s %q (%s)\n", match.Type, match.Name, match.UUID)
			return nil
		},
	}
	openCmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of a line")

	// doc reload — save + close + reopen a document. Exists because some
	// per-document engine state only refreshes on a real close/reopen: a
	// freshly CREATED PCB's pour reflow keeps using a creation-time rules
	// snapshot — rule writes and pour-rebuilds are ignored until the document
	// is reloaded (tab-switching away and back does NOT reload). The esp32-mini
	// playbook relies on this after its pour sequence.
	reloadCmd := &cobra.Command{
		Use:   "reload [name|uuid]",
		Short: "Save + close + reopen a document (default: the active one) — refreshes per-doc engine state",
		Long: `Save a document, close its tab, and reopen it — a real reload, unlike
"doc switch" which only changes the foreground tab.

Why: a freshly CREATED PCB document's copper-pour reflow keeps using the rules
snapshot taken at creation — writing rules (pcb drc-rules-set) and re-pouring
(pcb pour-rebuild) have NO effect until the document is closed and reopened.
After a reload the reflow honors the current rule configuration (clearance AND
thermal-spoke generation). Run "pcb pour-rebuild" after reloading a PCB.

The target document is saved first (schematic.save / pcb.save by type), so no
edits are lost. Defaults to the active document; pass a name/uuid to reload
another (it is brought to the front first).

The reopen leaves the reloaded document foreground, so reloading a NON-active
page would move the active tab. This command restores the pre-reload active
document afterward and reports it as "activeRestored", so the ★ does not drift
(issue #67).`,
		Args: cobra.MaximumNArgs(1),
		Example: `  easyeda doc reload                      # reload the active document
  easyeda doc reload PCB3 --project ceshi # reload a specific PCB
  easyeda pcb pour-rebuild                # then re-pour under the refreshed rules`,
		RunE: func(cmd *cobra.Command, args []string) error {
			startedAt := reloadNow()
			requested := ""
			if len(args) == 1 {
				requested = args[0]
			}
			failBeforeReload := func(stage, win string, err error) error {
				if jsonOut {
					r := &documentReloadReceipt{SchemaVersion: 1, Status: "failed", WindowID: win,
						Target: requested, StartedAt: startedAt, CompletedAt: reloadNow(),
						TimeSource: "cli-host-observation", FailedStage: stage, Error: err.Error(),
						Steps: []documentReloadStep{}}
					if writeErr := writeJSON(stdout, r); writeErr != nil {
						return writeErr
					}
				}
				return err
			}
			docs, activeUUID, win, err := discoverDocs(cfg, window)
			if err != nil {
				return failBeforeReload("discover", win, err)
			}
			target := activeUUID
			if len(args) == 1 {
				match, err := resolveDoc(docs, args[0])
				if err != nil {
					return failBeforeReload("resolve-target", win, err)
				}
				target = match.UUID
			}
			if target == "" {
				return failBeforeReload("resolve-target", win, fmt.Errorf("no active document to reload (run `easyeda doc ls`)"))
			}
			receipt, err := reloadDocumentWithReceipt(cfg, win, target, activeUUID)
			if jsonOut {
				if writeErr := writeJSON(stdout, receipt); writeErr != nil {
					return writeErr
				}
				return err
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "✓ reloaded %s %s (saved → closed → reopened)\n", receipt.DocumentType, target)
			if activeUUID != "" && activeUUID != target {
				fmt.Fprintf(stdout, "  ↩ restored active document to %s\n", receipt.ActiveRestored)
			}
			return nil
		},
	}
	reloadCmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of a line")

	doc.AddCommand(lsCmd, switchCmd, openCmd, reloadCmd, newDocReadyCmd(cfg, &window, stdout), newDocSourceCmd(cfg, &window, stdout))
	return doc
}

// discoverDocs resolves the target window ONCE, then aggregates
// schematic.pages.list + pcb.documents.list into a single openable-document list
// and marks the one matching the live active document (document.current). Every
// sub-call is pinned to the resolved windowId, so a second window appearing or a
// single-window auto-target racing mid-command can't break it. Returns the
// resolved windowId so a caller (e.g. `doc switch`) can pin its own follow-ups.
// PCB-listing failures are tolerated (a project may have no PCB).
func discoverDocs(cfg *appConfig, window string) (docs []openableDoc, activeUUID, resolvedWindow string, err error) {
	resolvedWindow, err = resolveTargetWindow(cfg, window)
	if err != nil {
		return nil, "", "", err
	}

	cur, err := requestAction(cfg, "document.current", resolvedWindow, nil)
	if err != nil && !isNoActiveDocument(cur) {
		return nil, "", "", err
	}
	if err == nil && cur.Context != nil {
		activeUUID = cur.Context.DocumentUUID
	}

	pages, err := requestAction(cfg, "schematic.pages.list", resolvedWindow, nil)
	if err != nil {
		return nil, "", "", err
	}
	for _, p := range mapsField(pages.Result, "pages") {
		docs = append(docs, openableDoc{
			UUID:   strField(p, "uuid"),
			Type:   "schematic",
			Name:   strField(p, "name"),
			Parent: strField(p, "parentSchematicUuid"),
		})
	}

	// PCBs are optional — a schematic-only project legitimately has none.
	if pcbs, perr := requestAction(cfg, "pcb.documents.list", resolvedWindow, nil); perr == nil {
		for _, p := range mapsField(pcbs.Result, "pcbs") {
			docs = append(docs, openableDoc{
				UUID:   strField(p, "uuid"),
				Type:   "pcb",
				Name:   strField(p, "name"),
				Parent: strField(p, "parentProjectUuid"),
			})
		}
	}

	// #190: the host may temporarily omit the active PCB from getAllPcbsInfo.
	// Recover its name from the independent current-PCB API, never from a cached
	// name or the user's selector. Both reads must identify the same document and
	// project; otherwise the user may have switched tabs during enumeration.
	if cur.Context != nil && cur.Context.DocumentType == "pcb" && activeUUID != "" {
		found := false
		for _, d := range docs {
			if d.UUID == activeUUID && d.Type == "pcb" {
				found = true
				break
			}
		}
		if !found {
			probeCfg := *cfg
			probeCfg.doc = "" // discovery must not recurse through the --doc guard
			info, ierr := requestAction(&probeCfg, "pcb.board.info", resolvedWindow, nil)
			if ierr != nil {
				return nil, "", "", fmt.Errorf("PCB document enumeration incomplete: active PCB %s is missing; current-PCB lookup failed: %w", activeUUID, ierr)
			}
			pcb, _ := info.Result["pcb"].(map[string]any)
			if info.Context == nil || cur.Context.ProjectUUID == "" ||
				info.Context.ProjectUUID != cur.Context.ProjectUUID ||
				info.Context.DocumentUUID != activeUUID || info.Context.DocumentType != "pcb" ||
				strField(pcb, "uuid") != activeUUID || strField(pcb, "name") == "" {
				return nil, "", "", fmt.Errorf("PCB document enumeration incomplete: current-PCB lookup does not confirm active PCB %s in the same project; refusing to guess the target", activeUUID)
			}
			docs = append(docs, openableDoc{UUID: activeUUID, Type: "pcb", Name: strField(pcb, "name"), Parent: cur.Context.ProjectUUID})
		}
	}

	for i := range docs {
		if docs[i].UUID != "" && docs[i].UUID == activeUUID {
			docs[i].Active = true
		}
	}
	sort.SliceStable(docs, func(i, j int) bool {
		if docs[i].Type != docs[j].Type {
			return docs[i].Type < docs[j].Type
		}
		return docs[i].Name < docs[j].Name
	})
	return docs, activeUUID, resolvedWindow, nil
}

// isNoActiveDocument recognizes the connector's explicit "the project is open,
// but no editor tab is active" result. That state is recoverable: the
// project-scoped schematic/PCB inventories remain readable and document.open
// can activate an inventory UUID. Keep the exception narrow so transport,
// project-context, and arbitrary document.current failures still fail closed.
//
// Connectors ≥1.11.1 report it as PRECONDITION_REFUSED (a state, excluded from
// write-health); older ones used the generic EDA_CALL_FAILED, so both codes are
// accepted and the stable message decides.
func isNoActiveDocument(res *actionResult) bool {
	if res == nil || (res.errorCode != "PRECONDITION_REFUSED" && res.errorCode != "EDA_CALL_FAILED") {
		return false
	}
	message := strings.TrimSpace(res.errorMsg)
	message = strings.TrimSuffix(message, ".")
	return strings.EqualFold(message, "No active document")
}

// resolveDoc maps a user-supplied name or uuid to exactly one openable doc.
// An exact uuid match wins; otherwise a case-insensitive name match is used.
// Ambiguous name matches return an error listing the candidates.
func resolveDoc(docs []openableDoc, target string) (openableDoc, error) {
	for _, d := range docs {
		if d.UUID == target {
			return d, nil
		}
	}
	var hits []openableDoc
	lt := strings.ToLower(target)
	for _, d := range docs {
		if strings.ToLower(d.Name) == lt {
			hits = append(hits, d)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return openableDoc{}, fmt.Errorf("no document named or with uuid %q (run `easyeda doc ls` to see options)", target)
	default:
		var names []string
		for _, h := range hits {
			names = append(names, fmt.Sprintf("%s/%s", h.Type, h.UUID))
		}
		return openableDoc{}, fmt.Errorf("%q is ambiguous: %s — pass a uuid", target, strings.Join(names, ", "))
	}
}

func printDocTable(w io.Writer, docs []openableDoc) {
	if len(docs) == 0 {
		fmt.Fprintln(w, "(no openable documents — is a project open in this window?)")
		return
	}
	fmt.Fprintf(w, "%-2s  %-9s  %-24s  %s\n", "", "TYPE", "NAME", "UUID")
	for _, d := range docs {
		marker := " "
		if d.Active {
			marker = "★"
		}
		fmt.Fprintf(w, "%-2s  %-9s  %-24s  %s\n", marker, d.Type, d.Name, d.UUID)
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// mapsField returns result[key] as a slice of string-keyed maps, tolerating the
// any-typed shape that survives JSON round-tripping.
func mapsField(result map[string]any, key string) []map[string]any {
	if result == nil {
		return nil
	}
	raw, ok := result[key].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func strField(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// reloadDocumentByUUID saves + closes + reopens document `target` in window
// `win` and waits for it to become the active document again — the extracted
// core of `doc reload`, shared with `pcb clear`'s verify pass (#121: some
// primitives are only enumerable after a real close/reopen, so a clear must
// reload before it can trust "the board is empty"). Brings the target to the
// foreground first when it isn't already. Returns the document's type
// ("pcb"/"schematic") so callers can branch.
func reloadDocumentByUUID(cfg *appConfig, win, target string) (string, error) {
	receipt, err := reloadDocumentWithReceipt(cfg, win, target, "")
	return receipt.DocumentType, err
}

func reloadDocumentObserved(cfg *appConfig, win, target string, receipt *documentReloadReceipt) (string, error) {
	cur, err := receipt.request(cfg, "inspect", "document.current", win, nil, defaultActionTimeout)
	if err != nil {
		return "", err
	}
	if cur.Context == nil || cur.Context.DocumentUUID != target || cur.Context.TabID == "" {
		if _, err := receipt.request(cfg, "activate", "document.open", win, map[string]any{"uuid": target}, defaultActionTimeout); err != nil {
			return "", err
		}
		cur, err = receipt.request(cfg, "inspect", "document.current", win, nil, defaultActionTimeout)
		if err != nil {
			return "", err
		}
		if cur.Context == nil || cur.Context.DocumentUUID != target || cur.Context.TabID == "" {
			return "", fmt.Errorf("could not activate document %s before reload (active=%v)", target, cur.Context)
		}
		if !waitDocSettleFor(cfg, win, cur.Context.DocumentType) {
			return cur.Context.DocumentType, fmt.Errorf("document %s became active before reload but its data did not settle", target)
		}
	}
	identity, identityErr := readyCurrentIdentity(cur, target, cfg.project)
	if identityErr != nil {
		return "", fmt.Errorf("reload target identity: %w", identityErr)
	}
	cur.Context = identity
	docType := cur.Context.DocumentType
	if cur.Context.ProjectUUID == "" || (docType != "pcb" && docType != "schematic") {
		return docType, fmt.Errorf("reload requires an identified schematic or PCB document")
	}
	projectBeforeReload := cur.Context.ProjectUUID
	saveAction := "schematic.save"
	if docType == "pcb" {
		saveAction = "pcb.save"
	}
	receipt.DocumentType = docType
	receipt.TargetContext = cur.Context
	saveResult, err := receipt.request(cfg, "save", saveAction, win, nil, defaultActionTimeout)
	if err != nil {
		return docType, fmt.Errorf("save before reload failed: %w", err)
	}
	if saveResult == nil || saveResult.Result["saved"] != true {
		return docType, fmt.Errorf("save before reload was not confirmed (saved:true required); document was not closed or reopened")
	}
	if !readyContextEqual(receipt.TargetContext, saveResult.Context) {
		return docType, fmt.Errorf("save response context differs from reload target; document not closed")
	}
	receipt.Saved = true
	// The typed close action verifies BOTH live identities and captures the
	// target split before closing. Looking the split up after close is racy: the
	// host can expose a transient blank tab or a tab from another split.
	closeRes, err := receipt.request(cfg, "close", "document.close", win, map[string]any{
		"uuid": target, "tabId": cur.Context.TabID,
	}, defaultActionTimeout)
	if err != nil {
		return docType, fmt.Errorf("close document failed: %w", err)
	}
	closed, _ := closeRes.Result["closed"].(bool)
	if !closed {
		return docType, fmt.Errorf("close document returned no success for %s; reopen was not attempted", target)
	}
	receipt.Closed = true
	splitScreenID, _ := closeRes.Result["splitScreenId"].(string)
	splitScreenID = strings.TrimSpace(splitScreenID)

	// closeDocument's promise can resolve before the editor has removed the old
	// active tab. Opening the same UUID during that interval is the second path
	// to the permanent loading spinner. Prove the old document is inactive; on
	// timeout, stop without issuing openDocument at all.
	closeDeadline := time.Now().Add(10 * time.Second)
	for {
		cur, err = receipt.request(cfg, "close-settle", "document.current", win, nil, 3*time.Second)
		if cur != nil && cur.Context != nil && cur.Context.ProjectUUID != "" && cur.Context.ProjectUUID != projectBeforeReload {
			return docType, fmt.Errorf("project changed while closing document; reopen was not attempted")
		}
		noActiveDocument := isNoActiveDocument(cur)
		if noActiveDocument {
			break
		}
		if err != nil {
			return docType, fmt.Errorf("close state could not be read; reopen was not attempted: %w", err)
		}
		afterClose, identityErr := readyCurrentIdentity(cur, "", projectBeforeReload)
		if identityErr != nil {
			return docType, fmt.Errorf("close state identity is unverified; reopen was not attempted: %w", identityErr)
		}
		if afterClose.DocumentUUID != target {
			break
		}
		if time.Now().After(closeDeadline) {
			return docType, fmt.Errorf("document %s close did not settle within 10s; reopen was not attempted to avoid a duplicate open — stop this operation and retain the error/context; do not issue a duplicate open", target)
		}
		time.Sleep(250 * time.Millisecond)
	}

	openPayload := map[string]any{"uuid": target}
	if splitScreenID != "" {
		openPayload["splitScreenId"] = splitScreenID
	}
	if _, err := receipt.request(cfg, "reopen", "document.open", win, openPayload, defaultActionTimeout); err != nil {
		return docType, fmt.Errorf("reopen after close failed for %s (the target tab was closed; reopen completion is unconfirmed and automatic retry was suppressed because the first open may still complete): %w — retain this failure and inspect typed connection/context evidence before any further write", target, err)
	}
	// The open call has succeeded; establish current identity and a stable full
	// component inventory with a separate bounded, navigation-free read phase.
	readyCfg := *cfg
	readyCfg.project, readyCfg.doc = projectBeforeReload, ""
	ready, err := receipt.observeReady(&readyCfg, win, target)
	if err != nil {
		return docType, fmt.Errorf("document %s reopened but its %s data did not settle: %w; stop writes and retain this failure/context", target, docType, err)
	}
	if ready.Active.DocumentType != docType {
		return docType, fmt.Errorf("document %s changed type after reopen", target)
	}
	receipt.Reloaded = target
	receipt.FinalContext = ready.Active
	return docType, nil
}
