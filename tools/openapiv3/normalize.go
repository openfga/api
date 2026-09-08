package main

import (
	"errors"
	"fmt"
	"strings"
)

type normalizationStats struct {
	removedOriginalParamNames int
	restoredRequiredFalse     int
}

// Swagger permits $ref siblings, but OpenAPI 3.0 ignores them; allOf preserves both meanings.
func normalizeRefSiblings(value any) (any, int, error) {
	switch value := value.(type) {
	case []any:
		count := 0
		for index, item := range value {
			normalized, itemCount, err := normalizeRefSiblings(item)
			if err != nil {
				return nil, 0, err
			}
			value[index] = normalized
			count += itemCount
		}
		return value, count, nil
	case object:
		count := 0
		for key, item := range value {
			normalized, itemCount, err := normalizeRefSiblings(item)
			if err != nil {
				return nil, 0, err
			}
			value[key] = normalized
			count += itemCount
		}
		ref, hasRef := value["$ref"]
		if hasRef && len(value) > 1 {
			if _, ok := ref.(string); !ok {
				return nil, 0, errors.New("$ref value must be a string")
			}
			siblings := make(object, len(value)-1)
			for key, item := range value {
				if key != "$ref" {
					siblings[key] = item
				}
			}
			return object{
				"allOf": []any{
					object{"$ref": ref},
					siblings,
				},
			}, count + 1, nil
		}
		return value, count, nil
	default:
		return value, 0, nil
	}
}

func normalizeOutput(value any) (normalizationStats, error) {
	stats := normalizationStats{}
	err := walk(value, "#", func(current object, location string) error {
		// kin-openapi adds this compatibility extension and omits explicit false values.
		if originalName, exists := current["x-originalParamName"]; exists {
			if originalName != "body" {
				return fmt.Errorf(
					"unexpected x-originalParamName value at %s: %v",
					location,
					originalName,
				)
			}
			delete(current, "x-originalParamName")
			stats.removedOriginalParamNames++
		}
		in, hasIn := current["in"].(string)
		_, hasName := current["name"].(string)
		if hasIn && hasName && in != "path" {
			if _, hasRequired := current["required"]; !hasRequired {
				current["required"] = false
				stats.restoredRequiredFalse++
			}
		}
		return nil
	})
	return stats, err
}

func countRefSiblings(value any) int {
	count := 0
	_ = walk(value, "#", func(current object, _ string) error {
		if _, hasRef := current["$ref"]; hasRef && len(current) > 1 {
			count++
		}
		return nil
	})
	return count
}

func rewriteSchemaRefs(value any) any {
	switch value := value.(type) {
	case []any:
		result := make([]any, len(value))
		for index, item := range value {
			result[index] = rewriteSchemaRefs(item)
		}
		return result
	case object:
		result := make(object, len(value))
		for key, item := range value {
			if key == "$ref" {
				result[key] = rewriteRef(item)
			} else {
				result[key] = rewriteSchemaRefs(item)
			}
		}
		return result
	default:
		return value
	}
}

func rewriteRef(value any) any {
	ref, ok := value.(string)
	if !ok {
		return value
	}
	replacements := []struct {
		from string
		to   string
	}{
		{from: "#/definitions/", to: "#/components/schemas/"},
		{from: "#/parameters/", to: "#/components/parameters/"},
		{from: "#/responses/", to: "#/components/responses/"},
	}
	for _, replacement := range replacements {
		if strings.HasPrefix(ref, replacement.from) {
			return strings.Replace(ref, replacement.from, replacement.to, 1)
		}
	}
	return ref
}
