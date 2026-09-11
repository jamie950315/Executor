package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuiltinContractDescriptionsAndByteBounds(t *testing.T) {
	for _, tool := range BuiltinTools() {
		properties := tool.InputSchema["properties"].(map[string]any)
		switch tool.Name {
		case "filesystem_read", "terminal_output":
			limit := properties["limit"].(map[string]any)
			if limit["maximum"] != 1048576 || limit["default"] != 65536 || !strings.Contains(limit["description"].(string), "bytes") {
				t.Errorf("%s limit schema = %#v", tool.Name, limit)
			}
			if len(tool.OutputSchema) == 0 {
				t.Errorf("%s has no output contract", tool.Name)
			}
		case "terminal":
			if !strings.Contains(tool.Description, "accepted") || !strings.Contains(tool.Description, "completion") {
				t.Errorf("terminal input acceptance/completion is ambiguous: %s", tool.Description)
			}
		case "filesystem_write":
			if len(tool.OutputSchema) == 0 {
				t.Error("filesystem_write has no output contract")
			}
		case "device_permissions":
			output := tool.OutputSchema["properties"].(map[string]any)
			if output["scope"] == nil || output["capabilities"] == nil || !strings.Contains(tool.Description, "desktop") {
				t.Errorf("permission scope is ambiguous: %#v", tool)
			}
		}
	}
}

func TestMutatingToolsExplicitlyExposeWriteAnnotations(t *testing.T) {
	for _, tool := range BuiltinTools() {
		if !tool.Annotations.DestructiveHint {
			continue
		}
		raw, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		annotations := value["annotations"].(map[string]any)
		if readOnly, exists := annotations["readOnlyHint"]; !exists || readOnly != false {
			t.Errorf("mutating tool %s has ambiguous read-only annotation: %#v", tool.Name, annotations)
		}
	}
}

func TestScreenshotExportHasAccurateSideEffectAnnotation(t *testing.T) {
	for _, tool := range BuiltinTools() {
		if tool.Name != "desktop_observe" {
			continue
		}
		if tool.Annotations.ReadOnlyHint || !tool.Annotations.DestructiveHint || !strings.Contains(tool.Description, "overwrite") {
			t.Fatalf("screenshot export side effect is hidden: %+v", tool)
		}
		return
	}
	t.Fatal("desktop_observe missing")
}
