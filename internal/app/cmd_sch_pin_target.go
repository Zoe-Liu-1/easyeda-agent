package app

// cmd_sch_pin_target.go — shared pin-target resolution for `sch connect`.
//
// `sch autoconnect` / `sch disconnect` address pins as `--pin DESIGNATOR:PIN`,
// and agents reach for the same spelling on `sch connect` — which historically
// only took raw `--x/--y` coordinates (a real misuse trap: same family, different
// parameter style). `sch connect` now accepts BOTH, mutually exclusive:
//
//	--pin U1:5      designator:pin — resolved to the pin coordinate via the
//	                same scene machinery autoconnect uses (components.list
//	                --include-pins → buildScene → resolvePinCoord), so off-page
//	                / ambiguous / typo'd refs get autoconnect's diagnostics
//	--x / --y       raw coordinates — legitimate on its own (primitives without
//	                a designator, exact-coordinate workflows), not a legacy path

import "fmt"

// validatePinTarget enforces the --pin XOR --x/--y contract for `sch connect`.
// pinSet reports --pin non-empty; xSet/ySet report the flags EXPLICITLY passed
// (cobra Changed, so a literal --y 0 counts). Pure function — unit-testable
// without a live editor.
func validatePinTarget(pinSet, xSet, ySet bool) error {
	switch {
	case pinSet && (xSet || ySet):
		return fmt.Errorf("--pin and --x/--y are mutually exclusive — pass ONE pin locator (--pin resolves the coordinate itself)")
	case pinSet:
		return nil
	case xSet && ySet:
		return nil
	case xSet || ySet:
		return fmt.Errorf("--x and --y must be given together (or use --pin DESIGNATOR:PIN)")
	default:
		return fmt.Errorf("pass --pin DESIGNATOR:PIN or both --x and --y")
	}
}

// resolveSchPin resolves a DESIGNATOR:PIN reference to the measured pin on
// the active schematic page, reusing autoconnect's scene builder so the error
// surface (off-page hint, ambiguity fan-out hint, pin-typo hint) is identical
// across the command family.
func resolveSchPin(cfg *appConfig, window, ref string) (acPin, error) {
	res, err := requestAction(cfg, "schematic.components.list", window, map[string]any{
		"includeBBox": true,
		"includePins": true, "includePinNets": false,
	})
	if err != nil {
		return acPin{}, err
	}
	return resolvePinCoord(buildScene(res.Result), ref)
}

// connectStubDirection picks the stub direction for `sch connect`: an explicit
// --direction wins; otherwise a resolved --pin leaves along its measured
// outward side (the same fact the geometry guard enforces), so the default can
// no longer point the stub back into the owning body. Empty means the pin side
// is unknown and the connector's per-kind default applies.
func connectStubDirection(explicit string, explicitSet bool, pin *acPin) string {
	if explicitSet {
		return explicit
	}
	if pin == nil {
		return ""
	}
	return outwardDirection(*pin)
}
