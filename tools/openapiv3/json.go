package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
)

type object = map[string]any

func collectExamples(value any) (map[string]int, int, error) {
	examples := make(map[string]int)
	total := 0
	err := walk(value, "#", func(current object, _ string) error {
		example, exists := current["example"]
		if !exists {
			return nil
		}
		encoded, err := json.Marshal(example)
		if err != nil {
			return err
		}
		examples[string(encoded)]++
		total++
		return nil
	})
	return examples, total, err
}

func walk(value any, location string, visit func(object, string) error) error {
	switch value := value.(type) {
	case []any:
		for index, item := range value {
			if err := walk(item, fmt.Sprintf("%s/%d", location, index), visit); err != nil {
				return err
			}
		}
	case object:
		if err := visit(value, location); err != nil {
			return err
		}
		for _, key := range sortedKeys(value) {
			if err := walk(value[key], location+"/"+escapeJSONPointer(key), visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolveJSONPointer(document any, ref string) (any, error) {
	if ref == "#" {
		return document, nil
	}
	current := document
	for _, token := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		decoded, err := url.PathUnescape(token)
		if err != nil {
			return nil, err
		}
		decoded = strings.ReplaceAll(strings.ReplaceAll(decoded, "~1", "/"), "~0", "~")
		switch value := current.(type) {
		case object:
			next, exists := value[decoded]
			if !exists {
				return nil, fmt.Errorf("missing object key %q", decoded)
			}
			current = next
		case []any:
			var index int
			if _, err := fmt.Sscanf(decoded, "%d", &index); err != nil || index < 0 || index >= len(value) {
				return nil, fmt.Errorf("invalid array index %q", decoded)
			}
			current = value[index]
		default:
			return nil, fmt.Errorf("cannot traverse %q", decoded)
		}
	}
	return current, nil
}

func requiredObject(container object, key, label string) (object, error) {
	value, exists := container[key]
	if !exists {
		return nil, fmt.Errorf("%s %s must be an object", label, key)
	}
	result, ok := value.(object)
	if !ok {
		return nil, fmt.Errorf("%s %s must be an object", label, key)
	}
	return result, nil
}

func requiredArray(container object, key, label string) ([]any, error) {
	value, exists := container[key]
	if !exists {
		return nil, fmt.Errorf("%s %s must be an array", label, key)
	}
	result, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s %s must be an array", label, key)
	}
	return result, nil
}

func decodeObject(data []byte) (object, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, errors.New("unexpected data after JSON document")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode trailing JSON data: %w", err)
	}
	result, ok := value.(object)
	if !ok {
		return nil, errors.New("document root must be an object")
	}
	return result, nil
}

func marshalJSON(value any, indent bool) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if indent {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func cloneJSON(value any) any {
	switch value := value.(type) {
	case []any:
		result := make([]any, len(value))
		for index, item := range value {
			result[index] = cloneJSON(item)
		}
		return result
	case object:
		result := make(object, len(value))
		for key, item := range value {
			result[key] = cloneJSON(item)
		}
		return result
	default:
		return value
	}
}

func equalJSON(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func sortedKeys(value object) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringArray(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func escapeJSONPointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func hasAnyKey(value object, keys ...string) bool {
	for _, key := range keys {
		if _, exists := value[key]; exists {
			return true
		}
	}
	return false
}

func hash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
