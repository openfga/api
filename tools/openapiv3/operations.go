package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var httpMethods = map[string]struct{}{
	"delete":  {},
	"get":     {},
	"head":    {},
	"options": {},
	"patch":   {},
	"post":    {},
	"put":     {},
	"trace":   {},
}

type operationInfo struct {
	apiPath       string
	method        string
	operation     object
	pathItem      object
	responseCodes []string
}

func validateParameterParity(v2Entry, v3Entry operationInfo) error {
	for _, pair := range []struct {
		label string
		v2    object
		v3    object
	}{
		{label: "path", v2: v2Entry.pathItem, v3: v3Entry.pathItem},
		{label: "operation", v2: v2Entry.operation, v3: v3Entry.operation},
	} {
		expected, err := expectedParameters(pair.v2)
		if err != nil {
			return fmt.Errorf("%s parameters for %s: %w", pair.label, operationLabel(v2Entry), err)
		}

		actual := any([]any{})
		if value, exists := pair.v3["parameters"]; exists {
			actual = value
		}
		if !equalJSON(expected, actual) {
			return fmt.Errorf("%s parameters changed for %s", pair.label, operationLabel(v2Entry))
		}
	}
	return nil
}

func countBodyParameters(operations map[string]operationInfo) int {
	count := 0
	for _, entry := range operations {
		for _, container := range []object{entry.pathItem, entry.operation} {
			values, _ := container["parameters"].([]any)
			for _, value := range values {
				parameter, ok := value.(object)
				if ok && parameter["in"] == "body" {
					count++
				}
			}
		}
	}
	return count
}

func countOptionalParameters(operations map[string]operationInfo) int {
	count := 0
	for _, entry := range operations {
		for _, container := range []object{entry.pathItem, entry.operation} {
			values, _ := container["parameters"].([]any)
			for _, value := range values {
				parameter, ok := value.(object)
				if !ok || parameter["in"] == "body" || parameter["in"] == "path" {
					continue
				}
				if required, _ := parameter["required"].(bool); !required {
					count++
				}
			}
		}
	}
	return count
}

func expectedParameters(container object) ([]any, error) {
	parameters, exists := container["parameters"]
	if !exists {
		return []any{}, nil
	}
	values, ok := parameters.([]any)
	if !ok {
		return nil, errors.New("parameters must be an array")
	}

	expected := make([]any, 0, len(values))
	for _, value := range values {
		parameter, ok := value.(object)
		if !ok {
			return nil, errors.New("parameter must be an object")
		}
		if parameter["in"] == "body" {
			continue
		}
		result := object{}
		for _, key := range []string{"name", "in", "description", "required"} {
			if field, exists := parameter[key]; exists {
				result[key] = cloneJSON(field)
			}
		}
		schema := object{}
		for _, key := range []string{
			"type",
			"format",
			"enum",
			"default",
			"items",
			"minimum",
			"maximum",
			"exclusiveMinimum",
			"exclusiveMaximum",
			"minLength",
			"maxLength",
			"pattern",
			"minItems",
			"maxItems",
			"uniqueItems",
			"multipleOf",
		} {
			if field, exists := parameter[key]; exists {
				schema[key] = rewriteSchemaRefs(field)
			}
		}
		result["schema"] = schema
		expected = append(expected, result)
	}
	return expected, nil
}

func validateRequestBodyParity(v2Entry, v3Entry operationInfo, rootConsumes []string) error {
	bodyParameters := make([]object, 0, 1)
	for _, container := range []object{v2Entry.pathItem, v2Entry.operation} {
		values, _ := container["parameters"].([]any)
		for _, value := range values {
			parameter, ok := value.(object)
			if ok && parameter["in"] == "body" {
				bodyParameters = append(bodyParameters, parameter)
			}
		}
	}
	if len(bodyParameters) > 1 {
		return fmt.Errorf("multiple body parameters found for %s", operationLabel(v2Entry))
	}

	actual, hasActual := v3Entry.operation["requestBody"]
	if len(bodyParameters) == 0 {
		if hasActual {
			return fmt.Errorf("OpenAPI v3 invented a request body for %s", operationLabel(v2Entry))
		}
		return nil
	}
	if !hasActual {
		return fmt.Errorf("OpenAPI v3 is missing the request body for %s", operationLabel(v2Entry))
	}

	body := bodyParameters[0]
	expected := object{}
	if description, ok := body["description"].(string); ok && description != "" {
		expected["description"] = description
	}
	if required, _ := body["required"].(bool); required {
		expected["required"] = true
	}
	schema, exists := body["schema"]
	if !exists {
		return fmt.Errorf("OpenAPI v2 body parameter lacks a schema for %s", operationLabel(v2Entry))
	}
	consumes := stringArray(v2Entry.operation["consumes"])
	if len(consumes) == 0 {
		consumes = rootConsumes
	}
	content := object{}
	for _, mediaType := range consumes {
		content[mediaType] = object{"schema": rewriteSchemaRefs(schema)}
	}
	expected["content"] = content

	if !equalJSON(expected, actual) {
		return fmt.Errorf("request body changed for %s", operationLabel(v2Entry))
	}
	return nil
}

func validateResponseParity(v2Entry, v3Entry operationInfo, rootProduces []string) error {
	v2Responses := v2Entry.operation["responses"].(object)
	v3Responses := v3Entry.operation["responses"].(object)
	produces := stringArray(v2Entry.operation["produces"])
	if len(produces) == 0 {
		produces = rootProduces
	}
	if len(produces) == 0 {
		produces = []string{"application/json"}
	}

	for code, value := range v2Responses {
		v2Response, ok := value.(object)
		if !ok {
			return fmt.Errorf("OpenAPI v2 response %s for %s must be an object", code, operationLabel(v2Entry))
		}
		actual, exists := v3Responses[code]
		if !exists {
			return fmt.Errorf("OpenAPI v3 response %s is missing for %s", code, operationLabel(v2Entry))
		}
		expected := object{}
		if ref, exists := v2Response["$ref"]; exists {
			expected["$ref"] = rewriteRef(ref)
		} else {
			expected["description"] = v2Response["description"]
			if schema, exists := v2Response["schema"]; exists {
				content := object{}
				for _, mediaType := range produces {
					content[mediaType] = object{"schema": rewriteSchemaRefs(schema)}
				}
				expected["content"] = content
			}
		}
		if !equalJSON(expected, actual) {
			return fmt.Errorf("response %s changed for %s", code, operationLabel(v2Entry))
		}
	}
	return nil
}

func collectOperations(document object, version string) (map[string]operationInfo, error) {
	paths, err := requiredObject(document, "paths", version)
	if err != nil {
		return nil, err
	}
	operations := make(map[string]operationInfo)
	operationIDs := make(map[string]string)

	for _, apiPath := range sortedKeys(paths) {
		if !strings.HasPrefix(apiPath, "/") {
			return nil, fmt.Errorf("%s path %q must start with \"/\"", version, apiPath)
		}
		pathItem, ok := paths[apiPath].(object)
		if !ok {
			return nil, fmt.Errorf("%s path item %s must be an object", version, apiPath)
		}
		for _, method := range sortedKeys(pathItem) {
			if _, isMethod := httpMethods[method]; !isMethod {
				continue
			}
			operation, ok := pathItem[method].(object)
			if !ok {
				return nil, fmt.Errorf(
					"%s operation %s %s must be an object",
					version,
					strings.ToUpper(method),
					apiPath,
				)
			}
			operationID, ok := operation["operationId"].(string)
			if !ok || operationID == "" {
				return nil, fmt.Errorf(
					"%s operation %s %s must have an operationId",
					version,
					strings.ToUpper(method),
					apiPath,
				)
			}
			if previous, duplicated := operationIDs[operationID]; duplicated {
				return nil, fmt.Errorf(
					"%s operationId %q is duplicated by %s and %s %s",
					version,
					operationID,
					previous,
					strings.ToUpper(method),
					apiPath,
				)
			}
			operationIDs[operationID] = strings.ToUpper(method) + " " + apiPath

			responses, err := requiredObject(operation, "responses", version+" operation "+operationID)
			if err != nil {
				return nil, err
			}
			responseCodes := make([]string, 0, len(responses))
			for code := range responses {
				if !strings.HasPrefix(code, "x-") {
					responseCodes = append(responseCodes, code)
				}
			}
			sort.Strings(responseCodes)
			if len(responseCodes) == 0 {
				return nil, fmt.Errorf("%s operation %s must define responses", version, operationID)
			}

			operations[apiPath+"\t"+method] = operationInfo{
				apiPath:       apiPath,
				method:        method,
				operation:     operation,
				pathItem:      pathItem,
				responseCodes: responseCodes,
			}
		}
	}
	return operations, nil
}

func operationLabel(entry operationInfo) string {
	return strings.ToUpper(entry.method) + " " + entry.apiPath
}
