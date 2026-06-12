package mcp

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/ephemera-ai/ephemera/internal/llm"
)

type objectSchema struct {
	Type                 string                     `json:"type"`
	Properties           map[string]json.RawMessage `json:"properties"`
	Required             []string                   `json:"required"`
	AdditionalProperties any                        `json:"additionalProperties"`
}

type propertySchema struct {
	Type        any    `json:"type"`
	Description string `json:"description"`
}

func providerSchema(raw json.RawMessage) llm.ToolSchema {
	result := llm.ToolSchema{Type: "object", Properties: map[string]llm.ToolProperty{}, AdditionalProperties: false}
	if len(raw) == 0 {
		return result
	}
	var schema objectSchema
	if json.Unmarshal(raw, &schema) != nil {
		return result
	}
	if schema.Type != "" {
		result.Type = schema.Type
	}
	result.Required = append([]string(nil), schema.Required...)
	if allow, ok := schema.AdditionalProperties.(bool); ok {
		result.AdditionalProperties = allow
	}
	for name, value := range schema.Properties {
		var property propertySchema
		_ = json.Unmarshal(value, &property)
		result.Properties[name] = llm.ToolProperty{Type: firstSchemaType(property.Type), Description: property.Description}
		if result.Properties[name].Type == "" {
			result.Properties[name] = llm.ToolProperty{Type: "string", Description: property.Description}
		}
	}
	return result
}

func firstSchemaType(value any) string {
	switch item := value.(type) {
	case string:
		return item
	case []any:
		for _, part := range item {
			if text, ok := part.(string); ok && text != "null" {
				return text
			}
		}
	}
	return ""
}

func validateArguments(raw json.RawMessage, arguments map[string]any) error {
	if arguments == nil {
		arguments = map[string]any{}
	}
	if len(raw) == 0 {
		return nil
	}
	var schema objectSchema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("invalid MCP input schema: %w", err)
	}
	for _, required := range schema.Required {
		value, ok := arguments[required]
		if !ok || value == nil || (reflect.TypeOf(value).Kind() == reflect.String && strings.TrimSpace(fmt.Sprint(value)) == "") {
			return fmt.Errorf("MCP tool requires %q", required)
		}
	}
	allowAdditional := true
	if value, ok := schema.AdditionalProperties.(bool); ok {
		allowAdditional = value
	}
	for name, value := range arguments {
		rawProperty, known := schema.Properties[name]
		if !known {
			if !allowAdditional {
				return fmt.Errorf("MCP tool does not accept argument %q", name)
			}
			continue
		}
		var property propertySchema
		if err := json.Unmarshal(rawProperty, &property); err != nil {
			continue
		}
		if err := validateValueType(name, firstSchemaType(property.Type), value); err != nil {
			return err
		}
	}
	return nil
}

func validateValueType(name, kind string, value any) error {
	if value == nil || kind == "" || kind == "null" {
		return nil
	}
	valid := false
	switch kind {
	case "string":
		_, valid = value.(string)
	case "boolean":
		_, valid = value.(bool)
	case "integer":
		switch number := value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
			valid = true
		case float64:
			valid = number == math.Trunc(number)
		}
	case "number":
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
			valid = true
		}
	case "object":
		_, valid = value.(map[string]any)
	case "array":
		_, valid = value.([]any)
	default:
		return nil
	}
	if !valid {
		return fmt.Errorf("MCP argument %q must be a %s", name, kind)
	}
	return nil
}
