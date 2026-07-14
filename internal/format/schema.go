package format

import (
	"encoding/json"
	"fmt"
	"strings"
)

var allowedSchemaFields = map[string]bool{
	"type": true, "description": true, "properties": true, "required": true,
	"items": true, "enum": true, "title": true,
}

var unsupportedSchemaFields = map[string]bool{
	"additionalProperties": true, "default": true, "$schema": true,
	"$defs": true, "definitions": true, "$ref": true, "$id": true,
	"$comment": true, "title": true, "minLength": true, "maxLength": true,
	"pattern": true, "format": true, "minItems": true, "maxItems": true,
	"examples": true, "allOf": true, "anyOf": true, "oneOf": true,
}

// SanitizeSchema mirrors the Node proxy's allowlist pass. Cloud Code's schema
// protobuf rejects arbitrary JSON Schema keywords and non-string enums.
func SanitizeSchema(value any) map[string]any {
	schema := asMap(value)
	if schema == nil {
		return placeholderSchema()
	}
	result := make(map[string]any)
	for key, raw := range schema {
		if key == "const" {
			switch raw.(type) {
			case string, bool, float64, float32, int, int32, int64:
				result["enum"] = []any{stringValue(raw)}
			default:
				encoded, _ := json.Marshal(raw)
				result = appendDescriptionHint(result, "Must be: "+string(encoded))
			}
			continue
		}
		if !allowedSchemaFields[key] {
			continue
		}
		switch key {
		case "enum":
			items := asSlice(raw)
			values := make([]any, 0, len(items))
			for _, item := range items {
				values = append(values, stringValue(item))
			}
			result[key] = values
		case "properties":
			properties := make(map[string]any)
			for name, property := range asMap(raw) {
				properties[name] = SanitizeSchema(property)
			}
			result[key] = properties
		case "items":
			if items := asSlice(raw); items != nil {
				cleaned := make([]any, 0, len(items))
				for _, item := range items {
					cleaned = append(cleaned, SanitizeSchema(item))
				}
				result[key] = cleaned
			} else {
				result[key] = SanitizeSchema(raw)
			}
		default:
			result[key] = cloneJSON(raw)
		}
	}
	if stringValue(result["type"]) == "" {
		result["type"] = "object"
	}
	if result["type"] == "object" && len(asMap(result["properties"])) == 0 {
		return mergeSchemaPlaceholder(result)
	}
	return result
}

func CleanSchema(value any) any {
	if items := asSlice(value); items != nil {
		result := make([]any, 0, len(items))
		for _, item := range items {
			result = append(result, CleanSchema(item))
		}
		return result
	}
	schema := asMap(value)
	if schema == nil {
		return value
	}
	// The Node cleaner's hint passes recurse before its final cleanup, whose
	// recursive cleanSchema calls run those passes again. Preserve that detail:
	// nested enum/constraint descriptions intentionally accumulate hints.
	result := cleanSchemaPhases(addSchemaHintsRecursive(cloneMap(schema)))
	return result
}

func cleanSchemaPhases(result map[string]any) map[string]any {
	if reference := stringValue(result["$ref"]); reference != "" {
		parts := strings.Split(reference, "/")
		name := parts[len(parts)-1]
		if name == "" {
			name = "unknown"
		}
		description := stringValue(result["description"])
		if description != "" {
			description += " (See: " + name + ")"
		} else {
			description = "See: " + name
		}
		return map[string]any{"type": "OBJECT", "description": description}
	}

	result = mergeAllOf(result)
	result = flattenUnion(result, "anyOf")
	result = flattenUnion(result, "oneOf")

	nullable := make(map[string]bool)
	if properties := asMap(result["properties"]); properties != nil {
		cleaned := make(map[string]any, len(properties))
		for name, raw := range properties {
			property := asMap(raw)
			if property != nil {
				property = flattenTypeArray(cloneMap(property), nullable, name)
				cleaned[name] = CleanSchema(property)
			} else {
				cleaned[name] = raw
			}
		}
		result["properties"] = cleaned
		if required := asSlice(result["required"]); required != nil {
			filtered := make([]any, 0, len(required))
			for _, rawName := range required {
				name := stringValue(rawName)
				if !nullable[name] {
					if _, exists := cleaned[name]; exists {
						filtered = append(filtered, rawName)
					}
				}
			}
			if len(filtered) == 0 {
				delete(result, "required")
			} else {
				result["required"] = filtered
			}
		}
	}
	if items := asSlice(result["items"]); items != nil {
		cleaned := make([]any, 0, len(items))
		for _, item := range items {
			cleaned = append(cleaned, CleanSchema(item))
		}
		result["items"] = cleaned
	} else if item := asMap(result["items"]); item != nil {
		result["items"] = cleanSchemaPhases(item)
	}
	for key := range unsupportedSchemaFields {
		delete(result, key)
	}
	if value, ok := result["type"].(string); ok {
		result["type"] = googleSchemaType(value)
	}
	return result
}

func addSchemaHintsRecursive(result map[string]any) map[string]any {
	if enum := asSlice(result["enum"]); len(enum) > 1 && len(enum) <= 10 {
		values := make([]string, 0, len(enum))
		for _, item := range enum {
			values = append(values, stringValue(item))
		}
		result = appendDescriptionHint(result, "Allowed: "+strings.Join(values, ", "))
	}
	if value, ok := result["additionalProperties"]; ok && value == false {
		result = appendDescriptionHint(result, "No extra properties allowed")
	}
	for _, constraint := range []string{"minLength", "maxLength", "pattern", "minimum", "maximum", "minItems", "maxItems", "format"} {
		if value, ok := result[constraint]; ok && asMap(value) == nil && asSlice(value) == nil {
			result = appendDescriptionHint(result, fmt.Sprintf("%s: %v", constraint, value))
		}
	}
	if properties := asMap(result["properties"]); properties != nil {
		cleaned := make(map[string]any, len(properties))
		for name, rawProperty := range properties {
			if property := asMap(rawProperty); property != nil {
				cleaned[name] = addSchemaHintsRecursive(property)
			} else {
				cleaned[name] = rawProperty
			}
		}
		result["properties"] = cleaned
	}
	if items := asSlice(result["items"]); items != nil {
		cleaned := make([]any, 0, len(items))
		for _, rawItem := range items {
			if item := asMap(rawItem); item != nil {
				cleaned = append(cleaned, addSchemaHintsRecursive(item))
			} else {
				cleaned = append(cleaned, rawItem)
			}
		}
		result["items"] = cleaned
	} else if item := asMap(result["items"]); item != nil {
		result["items"] = addSchemaHintsRecursive(item)
	}
	return result
}

func mergeAllOf(result map[string]any) map[string]any {
	items := asSlice(result["allOf"])
	if len(items) == 0 {
		return result
	}
	mergedProperties := make(map[string]any)
	requiredSeen := make(map[string]bool)
	required := make([]any, 0)
	other := make(map[string]any)
	for _, raw := range items {
		sub := asMap(raw)
		for name, property := range asMap(sub["properties"]) {
			mergedProperties[name] = property
		}
		for _, name := range asSlice(sub["required"]) {
			text := stringValue(name)
			if !requiredSeen[text] {
				requiredSeen[text] = true
				required = append(required, name)
			}
		}
		for key, value := range sub {
			if key != "properties" && key != "required" {
				if _, exists := other[key]; !exists {
					other[key] = value
				}
			}
		}
	}
	delete(result, "allOf")
	for key, value := range other {
		if _, exists := result[key]; !exists {
			result[key] = value
		}
	}
	if parent := asMap(result["properties"]); len(mergedProperties) > 0 || len(parent) > 0 {
		for name, value := range parent {
			mergedProperties[name] = value
		}
		result["properties"] = mergedProperties
	}
	for _, name := range asSlice(result["required"]) {
		text := stringValue(name)
		if !requiredSeen[text] {
			requiredSeen[text] = true
			required = append(required, name)
		}
	}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}

func flattenUnion(result map[string]any, key string) map[string]any {
	options := asSlice(result[key])
	if len(options) == 0 {
		return result
	}
	types := make([]string, 0)
	bestScore := -1
	var best map[string]any
	for _, raw := range options {
		option := asMap(raw)
		if option == nil {
			continue
		}
		typeName := stringValue(option["type"])
		if typeName == "" && option["properties"] != nil {
			typeName = "object"
		}
		if typeName != "" && typeName != "null" {
			types = append(types, typeName)
		}
		score := schemaScore(option)
		if score > bestScore {
			bestScore, best = score, option
		}
	}
	delete(result, key)
	if best != nil {
		parentDescription := stringValue(result["description"])
		for field, value := range cleanSchemaPhases(cloneMap(best)) {
			if field == "description" && stringValue(value) != parentDescription {
				if parentDescription == "" {
					result[field] = value
				} else {
					result[field] = parentDescription + " (" + stringValue(value) + ")"
				}
			} else if _, exists := result[field]; !exists || field == "type" || field == "properties" || field == "items" {
				result[field] = value
			}
		}
	}
	unique := uniqueStrings(types)
	if len(unique) > 1 {
		result = appendDescriptionHint(result, "Accepts: "+strings.Join(unique, " | "))
	}
	return result
}

func flattenTypeArray(result map[string]any, nullable map[string]bool, name string) map[string]any {
	types := asSlice(result["type"])
	if types == nil {
		return result
	}
	nonNull := make([]string, 0)
	hasNull := false
	for _, raw := range types {
		value := stringValue(raw)
		if value == "null" {
			hasNull = true
		} else if value != "" {
			nonNull = append(nonNull, value)
		}
	}
	if len(nonNull) == 0 {
		result["type"] = "string"
	} else {
		result["type"] = nonNull[0]
	}
	if len(nonNull) > 1 {
		result = appendDescriptionHint(result, "Accepts: "+strings.Join(nonNull, " | "))
	}
	if hasNull {
		result = appendDescriptionHint(result, "nullable")
		nullable[name] = true
	}
	return result
}

func appendDescriptionHint(schema map[string]any, hint string) map[string]any {
	result := cloneMap(schema)
	if description := stringValue(result["description"]); description != "" {
		result["description"] = description + " (" + hint + ")"
	} else {
		result["description"] = hint
	}
	return result
}

func schemaScore(schema map[string]any) int {
	if schema["type"] == "object" || schema["properties"] != nil {
		return 3
	}
	if schema["type"] == "array" || schema["items"] != nil {
		return 2
	}
	if value := stringValue(schema["type"]); value != "" && value != "null" {
		return 1
	}
	return 0
}

func googleSchemaType(value string) string {
	switch strings.ToLower(value) {
	case "string", "null":
		return "STRING"
	case "number":
		return "NUMBER"
	case "integer":
		return "INTEGER"
	case "boolean":
		return "BOOLEAN"
	case "array":
		return "ARRAY"
	case "object":
		return "OBJECT"
	default:
		return strings.ToUpper(value)
	}
}

func placeholderSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{"reason": map[string]any{
			"type": "string", "description": "Reason for calling this tool",
		}},
		"required": []any{"reason"},
	}
}

func mergeSchemaPlaceholder(result map[string]any) map[string]any {
	placeholder := placeholderSchema()
	result["properties"] = placeholder["properties"]
	result["required"] = placeholder["required"]
	return result
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneJSON(value)
	}
	return result
}

func cloneJSON(value any) any {
	if object := asMap(value); object != nil {
		return cloneMap(object)
	}
	if items := asSlice(value); items != nil {
		result := make([]any, len(items))
		for index, item := range items {
			result[index] = cloneJSON(item)
		}
		return result
	}
	return value
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
