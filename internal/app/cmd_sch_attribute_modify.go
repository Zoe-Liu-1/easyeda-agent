package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	"github.com/spf13/cobra"
)

func newSchAttributeModifyCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var from string
	cmd := &cobra.Command{
		Use:   "attribute-modify --from <patch.json>",
		Short: "Modify rotation/visibility/value on one bound schematic attribute",
		Long: `Modify one existing schematic Name, Designator or Value attribute using a
strict patch containing only rotation, valueVisible, keyVisible and/or value.
The JSON payload also carries the exact project/document identity and complete
original native attribute state.

Fresh before/after reads must prove the requested fields landed while every
unrequested field stayed unchanged. Partial or unverified writes retain evidence
and are never retried automatically.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(from) == "" {
				return fmt.Errorf("--from is required")
			}
			data, err := os.ReadFile(from)
			if err != nil {
				return err
			}
			var payload map[string]any
			if err := json.Unmarshal(data, &payload); err != nil {
				return err
			}
			if err := validateSchAttributeModifyPayload(payload); err != nil {
				return err
			}
			return dispatch(cfg, "schematic.attribute.modify", *window, payload, stdout, stderr)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "JSON identity + complete expected state + strict property patch")
	return cmd
}

func validateSchAttributeModifyPayload(payload map[string]any) error {
	if len(payload) != 5 {
		return fmt.Errorf("attribute modify requires only projectUuid, documentUuid, primitiveId, expected and patch")
	}
	for _, key := range []string{"projectUuid", "documentUuid", "primitiveId"} {
		if strings.TrimSpace(strField(payload, key)) == "" {
			return fmt.Errorf("attribute modify requires %s", key)
		}
	}
	expected, ok := payload["expected"].(map[string]any)
	if !ok {
		return fmt.Errorf("attribute modify requires complete expected state")
	}
	if err := validateSchAttributeState(expected); err != nil {
		return err
	}
	if expected["primitiveId"] != payload["primitiveId"] {
		return fmt.Errorf("attribute modify ID differs from expected state")
	}
	patch, ok := payload["patch"].(map[string]any)
	if !ok || len(patch) == 0 {
		return fmt.Errorf("attribute modify patch must be a non-empty object")
	}
	for key, value := range patch {
		switch key {
		case "rotation":
			if value != nil && !schAttributeFinite(value) {
				return fmt.Errorf("attribute modify rotation must be finite or null")
			}
		case "valueVisible", "keyVisible":
			if value != nil {
				if _, ok := value.(bool); !ok {
					return fmt.Errorf("attribute modify %s must be boolean or null", key)
				}
			}
		case "value":
			if _, ok := value.(string); !ok {
				return fmt.Errorf("attribute modify value must be a string")
			}
		default:
			return fmt.Errorf("attribute modify does not allow patch.%s", key)
		}
	}
	return nil
}

func schAttributeModifyResultError(action string, result map[string]any) error {
	if action != "schematic.attribute.modify" {
		return nil
	}
	fail := fmt.Errorf("schematic.attribute.modify: properties are not fully verified")
	if result["scope"] != "attribute-properties" || result["verified"] != true ||
		result["partial"] != false || result["primitiveId"] == "" {
		return fail
	}
	before, beforeOK := result["before"].(map[string]any)
	after, afterOK := result["after"].(map[string]any)
	requested, reqOK := result["requested"].(map[string]any)
	if !beforeOK || !afterOK || !reqOK || len(requested) == 0 ||
		validateSchAttributeState(before) != nil || validateSchAttributeState(after) != nil {
		return fail
	}
	want := map[string]any{}
	for key, value := range before {
		want[key] = value
	}
	for key, value := range requested {
		stateKey := ""
		switch key {
		case "rotation":
			stateKey = "Rotation"
		case "valueVisible":
			stateKey = "ValueVisible"
		case "keyVisible":
			stateKey = "KeyVisible"
		case "value":
			stateKey = "Value"
		default:
			return fail
		}
		want[stateKey] = value
	}
	if !reflect.DeepEqual(after, want) {
		return fail
	}
	return nil
}
