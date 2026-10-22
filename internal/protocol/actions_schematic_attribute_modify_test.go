package protocol

import "testing"

// The connector and CLI can both ship an action while the daemon's catalog
// omits it. In that state every call is rejected locally with UNKNOWN_ACTION
// before reaching the connector, so pin the mutating catalog contract.
func TestSchematicAttributeModifyRegistered(t *testing.T) {
	for _, action := range AllActions() {
		if action.Name != "schematic.attribute.modify" {
			continue
		}
		if !action.Mutates {
			t.Error("schematic.attribute.modify writes native attribute state; Mutates must be true")
		}
		if !action.NeedsWindow {
			t.Error("schematic.attribute.modify needs a connected EasyEDA window")
		}
		if action.Domain != DomainSchematic {
			t.Errorf("domain = %q, want %q", action.Domain, DomainSchematic)
		}
		return
	}
	t.Fatal("AllActions() is missing schematic.attribute.modify; the daemon will reject it with UNKNOWN_ACTION")
}
