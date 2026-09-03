package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
)

const (
	expectedInternalRefs   = 330
	expectedRefSiblings    = 43
	openAPIV2DefaultPath   = "../../docs/openapiv2/apidocs.swagger.json"
	openAPIV3DefaultPath   = "../../docs/openapiv3/apidocs.openapi.json"
	openAPIV3TargetVersion = "3.0.3"
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

type object = map[string]any

type operationInfo struct {
	apiPath       string
	method        string
	operation     object
	pathItem      object
	responseCodes []string
}

type normalizationStats struct {
	removedOriginalParamNames int
	restoredRequiredFalse     int
}

type generationSummary struct {
	examples      int
	operations    int
	paths         int
	refs          int
	requestBodies int
	schemas       int
}

func main() {
	sourcePath := flag.String("source", openAPIV2DefaultPath, "path to the finalized Swagger 2 document")
	outputPath := flag.String("output", openAPIV3DefaultPath, "path for the generated OpenAPI 3 document")
	flag.Parse()

	summary, sourceHash, err := run(*sourcePath, *outputPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf(
		"Generated OpenAPI %s: %d paths, %d operations, %d schemas, %d examples, %d internal refs\n",
		openAPIV3TargetVersion,
		summary.paths,
		summary.operations,
		summary.schemas,
		summary.examples,
		summary.refs,
	)
	fmt.Printf("OpenAPI v2 SHA-256 unchanged: %s\n", sourceHash)
}

func run(sourcePath, outputPath string) (generationSummary, string, error) {
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		return generationSummary{}, "", fmt.Errorf("read OpenAPI v2 source: %w", err)
	}
	sourceHash := hash(sourceBytes)

	output, summary, err := generate(sourceBytes)
	if err != nil {
		return generationSummary{}, "", err
	}

	sourceBytesAfterConversion, err := os.ReadFile(sourcePath)
	if err != nil {
		return generationSummary{}, "", fmt.Errorf("re-read OpenAPI v2 source: %w", err)
	}
	if sourceHash != hash(sourceBytesAfterConversion) || !bytes.Equal(sourceBytes, sourceBytesAfterConversion) {
		return generationSummary{}, "", errors.New("OpenAPI v2 artifact changed during OpenAPI v3 conversion")
	}

	if err := writeAtomically(outputPath, output); err != nil {
		return generationSummary{}, "", err
	}
	return summary, sourceHash, nil
}

func generate(sourceBytes []byte) ([]byte, generationSummary, error) {
	openAPIV2, err := decodeObject(sourceBytes)
	if err != nil {
		return nil, generationSummary{}, fmt.Errorf("decode OpenAPI v2 source: %w", err)
	}
	v2Operations, err := validateOpenAPIV2(openAPIV2)
	if err != nil {
		return nil, generationSummary{}, err
	}
	expectedRequestBodies := countBodyParameters(v2Operations)
	expectedRequiredFalse := countOptionalParameters(v2Operations)

	normalizedV2, err := decodeObject(sourceBytes)
	if err != nil {
		return nil, generationSummary{}, fmt.Errorf("decode OpenAPI v2 source for normalization: %w", err)
	}
	normalizedValue, refSiblingCount, err := normalizeRefSiblings(normalizedV2)
	if err != nil {
		return nil, generationSummary{}, err
	}
	if refSiblingCount != expectedRefSiblings {
		return nil, generationSummary{}, fmt.Errorf(
			"OpenAPI v2 ref sibling count changed from %d to %d",
			expectedRefSiblings,
			refSiblingCount,
		)
	}
	normalizedV2 = normalizedValue.(object)
	if countRefSiblings(normalizedV2) != 0 {
		return nil, generationSummary{}, errors.New("OpenAPI v2 ref sibling normalization was incomplete")
	}

	normalizedBytes, err := marshalJSON(normalizedV2, false)
	if err != nil {
		return nil, generationSummary{}, fmt.Errorf("encode normalized OpenAPI v2 source: %w", err)
	}
	var typedV2 openapi2.T
	if err := json.Unmarshal(normalizedBytes, &typedV2); err != nil {
		return nil, generationSummary{}, fmt.Errorf("decode normalized OpenAPI v2 source: %w", err)
	}

	typedV3, err := openapi2conv.ToV3(&typedV2)
	if err != nil {
		return nil, generationSummary{}, fmt.Errorf("convert OpenAPI v2 to v3: %w", err)
	}
	convertedBytes, err := json.Marshal(typedV3)
	if err != nil {
		return nil, generationSummary{}, fmt.Errorf("encode converted OpenAPI v3 document: %w", err)
	}
	openAPIV3, err := decodeObject(convertedBytes)
	if err != nil {
		return nil, generationSummary{}, fmt.Errorf("decode converted OpenAPI v3 document: %w", err)
	}

	normalization, err := normalizeOutput(openAPIV3)
	if err != nil {
		return nil, generationSummary{}, err
	}
	if normalization.removedOriginalParamNames != expectedRequestBodies {
		return nil, generationSummary{}, fmt.Errorf(
			"converter-only request body extension count changed from %d to %d",
			expectedRequestBodies,
			normalization.removedOriginalParamNames,
		)
	}
	if normalization.restoredRequiredFalse != expectedRequiredFalse {
		return nil, generationSummary{}, fmt.Errorf(
			"optional parameter normalization count changed from %d to %d",
			expectedRequiredFalse,
			normalization.restoredRequiredFalse,
		)
	}

	summary, err := validateConverted(openAPIV2, normalizedV2, openAPIV3, v2Operations)
	if err != nil {
		return nil, generationSummary{}, err
	}

	output, err := marshalJSON(openAPIV3, true)
	if err != nil {
		return nil, generationSummary{}, fmt.Errorf("encode final OpenAPI v3 document: %w", err)
	}
	return output, summary, nil
}

func validateConverted(
	originalV2 object,
	normalizedV2 object,
	openAPIV3 object,
	v2Operations map[string]operationInfo,
) (generationSummary, error) {
	v3Operations, summary, err := validateOpenAPIV3(openAPIV3)
	if err != nil {
		return generationSummary{}, err
	}
	if err := validateParity(originalV2, normalizedV2, openAPIV3, v2Operations, v3Operations); err != nil {
		return generationSummary{}, err
	}
	return summary, nil
}

func validateOpenAPIV2(document object) (map[string]operationInfo, error) {
	if document["swagger"] != "2.0" {
		return nil, errors.New(`OpenAPI v2 root "swagger" must be "2.0"`)
	}
	if _, exists := document["openapi"]; exists {
		return nil, errors.New(`OpenAPI v2 root must not contain "openapi"`)
	}
	if _, err := requiredObject(document, "info", "OpenAPI v2"); err != nil {
		return nil, err
	}
	paths, err := requiredObject(document, "paths", "OpenAPI v2")
	if err != nil {
		return nil, err
	}
	definitions, err := requiredObject(document, "definitions", "OpenAPI v2")
	if err != nil {
		return nil, err
	}
	if _, err := requiredArray(document, "tags", "OpenAPI v2"); err != nil {
		return nil, err
	}
	operations, err := collectOperations(document, "OpenAPI v2")
	if err != nil {
		return nil, err
	}
	if err := validateServiceCoverage(document, operations, "OpenAPI v2"); err != nil {
		return nil, err
	}
	if err := validateContentFree204(operations, "OpenAPI v2"); err != nil {
		return nil, err
	}
	refCount, err := validateInternalRefs(document, "OpenAPI v2")
	if err != nil {
		return nil, err
	}
	if refCount != expectedInternalRefs {
		return nil, fmt.Errorf(
			"OpenAPI v2 internal ref count changed from %d to %d",
			expectedInternalRefs,
			refCount,
		)
	}
	_, exampleCount, err := collectExamples(document)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 || len(definitions) == 0 || len(operations) == 0 || exampleCount == 0 {
		return nil, errors.New("OpenAPI v2 document must contain paths, operations, schemas, and examples")
	}
	return operations, nil
}

func validateOpenAPIV3(document object) (map[string]operationInfo, generationSummary, error) {
	if document["openapi"] != openAPIV3TargetVersion {
		return nil, generationSummary{}, fmt.Errorf(
			`OpenAPI v3 root "openapi" must be %q`,
			openAPIV3TargetVersion,
		)
	}
	if _, err := requiredObject(document, "info", "OpenAPI v3"); err != nil {
		return nil, generationSummary{}, err
	}
	paths, err := requiredObject(document, "paths", "OpenAPI v3")
	if err != nil {
		return nil, generationSummary{}, err
	}
	components, err := requiredObject(document, "components", "OpenAPI v3")
	if err != nil {
		return nil, generationSummary{}, err
	}
	schemas, err := requiredObject(components, "schemas", "OpenAPI v3 components")
	if err != nil {
		return nil, generationSummary{}, err
	}
	if _, err := requiredArray(document, "tags", "OpenAPI v3"); err != nil {
		return nil, generationSummary{}, err
	}
	for _, key := range []string{
		"swagger",
		"definitions",
		"parameters",
		"responses",
		"securityDefinitions",
		"schemes",
		"consumes",
		"produces",
	} {
		if _, exists := document[key]; exists {
			return nil, generationSummary{}, fmt.Errorf(
				"OpenAPI v3 root contains Swagger-only key %q",
				key,
			)
		}
	}
	operations, err := collectOperations(document, "OpenAPI v3")
	if err != nil {
		return nil, generationSummary{}, err
	}
	if err := validateNoSwaggerOnlyV3(document, operations); err != nil {
		return nil, generationSummary{}, err
	}
	if err := validateServiceCoverage(document, operations, "OpenAPI v3"); err != nil {
		return nil, generationSummary{}, err
	}
	if err := validateContentFree204(operations, "OpenAPI v3"); err != nil {
		return nil, generationSummary{}, err
	}
	refCount, err := validateInternalRefs(document, "OpenAPI v3")
	if err != nil {
		return nil, generationSummary{}, err
	}
	if refCount != expectedInternalRefs {
		return nil, generationSummary{}, fmt.Errorf(
			"OpenAPI v3 internal ref count changed from %d to %d",
			expectedInternalRefs,
			refCount,
		)
	}
	if siblingCount := countRefSiblings(document); siblingCount != 0 {
		return nil, generationSummary{}, fmt.Errorf(
			"OpenAPI v3 contains %d ref objects with siblings",
			siblingCount,
		)
	}
	_, exampleCount, err := collectExamples(document)
	if err != nil {
		return nil, generationSummary{}, err
	}
	requestBodyCount := 0
	for _, operation := range operations {
		if _, exists := operation.operation["requestBody"]; exists {
			requestBodyCount++
		}
	}
	return operations, generationSummary{
		examples:      exampleCount,
		operations:    len(operations),
		paths:         len(paths),
		refs:          refCount,
		requestBodies: requestBodyCount,
		schemas:       len(schemas),
	}, nil
}

func validateParity(
	originalV2 object,
	normalizedV2 object,
	openAPIV3 object,
	v2Operations map[string]operationInfo,
	v3Operations map[string]operationInfo,
) error {
	v2Paths := sortedKeys(originalV2["paths"].(object))
	v3Paths := sortedKeys(openAPIV3["paths"].(object))
	if !equalJSON(v2Paths, v3Paths) {
		return errors.New("API paths changed during OpenAPI v3 conversion")
	}
	if len(v2Operations) != len(v3Operations) {
		return fmt.Errorf(
			"operation count changed from %d to %d",
			len(v2Operations),
			len(v3Operations),
		)
	}
	if len(originalV2["paths"].(object)) != len(openAPIV3["paths"].(object)) {
		return errors.New("path count changed during OpenAPI v3 conversion")
	}

	normalizedOperations, err := collectOperations(normalizedV2, "normalized OpenAPI v2")
	if err != nil {
		return err
	}
	rootConsumes := stringArray(normalizedV2["consumes"])
	rootProduces := stringArray(normalizedV2["produces"])
	for key, v2Entry := range v2Operations {
		v3Entry, exists := v3Operations[key]
		if !exists {
			return fmt.Errorf(
				"OpenAPI v3 is missing %s %s",
				strings.ToUpper(v2Entry.method),
				v2Entry.apiPath,
			)
		}
		for _, property := range []string{"operationId", "summary", "description", "tags"} {
			if !equalJSON(v2Entry.operation[property], v3Entry.operation[property]) {
				return fmt.Errorf(
					"%s changed for %s %s",
					property,
					strings.ToUpper(v2Entry.method),
					v2Entry.apiPath,
				)
			}
		}
		if !equalJSON(v2Entry.responseCodes, v3Entry.responseCodes) {
			return fmt.Errorf(
				"response codes changed for %s %s",
				strings.ToUpper(v2Entry.method),
				v2Entry.apiPath,
			)
		}

		normalizedEntry := normalizedOperations[key]
		if err := validateParameterParity(normalizedEntry, v3Entry); err != nil {
			return err
		}
		if err := validateRequestBodyParity(normalizedEntry, v3Entry, rootConsumes); err != nil {
			return err
		}
		if err := validateResponseParity(normalizedEntry, v3Entry, rootProduces); err != nil {
			return err
		}
	}

	v2Examples, _, err := collectExamples(originalV2)
	if err != nil {
		return err
	}
	v3Examples, _, err := collectExamples(openAPIV3)
	if err != nil {
		return err
	}
	if !equalJSON(v2Examples, v3Examples) {
		return errors.New("examples changed during OpenAPI v3 conversion")
	}

	v2Definitions := normalizedV2["definitions"].(object)
	v3Components := openAPIV3["components"].(object)
	v3Schemas := v3Components["schemas"].(object)
	expectedSchemas := rewriteSchemaRefs(v2Definitions)
	if !equalJSON(expectedSchemas, v3Schemas) {
		return errors.New("component schemas changed during OpenAPI v3 conversion")
	}
	if len(v2Definitions) != len(v3Schemas) {
		return errors.New("component schema count changed during OpenAPI v3 conversion")
	}
	if !equalJSON(originalV2["tags"], openAPIV3["tags"]) {
		return errors.New("root tags changed during OpenAPI v3 conversion")
	}
	if !equalJSON(originalV2["info"], openAPIV3["info"]) {
		return errors.New("API information changed during OpenAPI v3 conversion")
	}

	if !hasAnyKey(originalV2, "host", "basePath") {
		if _, exists := openAPIV3["servers"]; exists {
			return errors.New("OpenAPI v3 conversion invented servers")
		}
	}
	if _, exists := originalV2["securityDefinitions"]; !exists {
		if _, exists := v3Components["securitySchemes"]; exists {
			return errors.New("OpenAPI v3 conversion invented security schemes")
		}
	}
	if _, exists := originalV2["security"]; !exists {
		if _, exists := openAPIV3["security"]; exists {
			return errors.New("OpenAPI v3 conversion invented root security policy")
		}
	}
	return nil
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

func validateNoSwaggerOnlyV3(document object, operations map[string]operationInfo) error {
	for _, entry := range operations {
		for _, key := range []string{"consumes", "produces", "schemes"} {
			if _, exists := entry.operation[key]; exists {
				return fmt.Errorf(
					"OpenAPI v3 operation %s contains Swagger-only key %q",
					operationLabel(entry),
					key,
				)
			}
		}
		for _, container := range []object{entry.pathItem, entry.operation} {
			parameters, _ := container["parameters"].([]any)
			for _, value := range parameters {
				parameter, ok := value.(object)
				if !ok {
					return fmt.Errorf("OpenAPI v3 parameter for %s must be an object", operationLabel(entry))
				}
				if parameter["in"] == "body" || parameter["in"] == "formData" {
					return fmt.Errorf(
						"OpenAPI v3 operation %s contains a Swagger-only %s parameter",
						operationLabel(entry),
						parameter["in"],
					)
				}
				for _, key := range []string{"type", "format", "items", "collectionFormat"} {
					if _, exists := parameter[key]; exists {
						return fmt.Errorf(
							"OpenAPI v3 parameter for %s contains Swagger-only key %q",
							operationLabel(entry),
							key,
						)
					}
				}
			}
		}
		responses := entry.operation["responses"].(object)
		for code, value := range responses {
			if strings.HasPrefix(code, "x-") {
				continue
			}
			response, ok := value.(object)
			if !ok {
				return fmt.Errorf(
					"OpenAPI v3 response %s for %s must be an object",
					code,
					operationLabel(entry),
				)
			}
			for _, key := range []string{"schema", "examples"} {
				if _, exists := response[key]; exists {
					return fmt.Errorf(
						"OpenAPI v3 response %s for %s contains Swagger-only key %q",
						code,
						operationLabel(entry),
						key,
					)
				}
			}
		}
	}

	return walk(document, "#", func(value object, location string) error {
		for key := range value {
			if strings.HasPrefix(strings.ToLower(key), "x-mintlify") {
				return fmt.Errorf(
					"OpenAPI v3 contains Mintlify-specific extension %q at %s",
					key,
					location,
				)
			}
		}
		if ref, ok := value["$ref"].(string); ok && strings.HasPrefix(ref, "#/definitions/") {
			return fmt.Errorf("OpenAPI v3 contains a Swagger-only definition reference at %s", location)
		}
		if _, exists := value["x-originalParamName"]; exists {
			return fmt.Errorf("OpenAPI v3 contains converter-only extension at %s", location)
		}
		return nil
	})
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

func validateInternalRefs(document object, version string) (int, error) {
	count := 0
	err := walk(document, "#", func(value object, location string) error {
		refValue, exists := value["$ref"]
		if !exists {
			return nil
		}
		count++
		ref, ok := refValue.(string)
		if !ok {
			return fmt.Errorf("%s $ref at %s must be a string", version, location)
		}
		if ref != "#" && !strings.HasPrefix(ref, "#/") {
			return fmt.Errorf("%s $ref at %s must be internal: %s", version, location, ref)
		}
		if _, err := resolveJSONPointer(document, ref); err != nil {
			return fmt.Errorf("%s $ref at %s does not resolve: %s: %w", version, location, ref, err)
		}
		return nil
	})
	return count, err
}

func validateServiceCoverage(document object, operations map[string]operationInfo, version string) error {
	tags, err := requiredArray(document, "tags", version)
	if err != nil {
		return err
	}
	rootTags := make(map[string]struct{})
	for _, value := range tags {
		tag, ok := value.(object)
		if !ok {
			return fmt.Errorf("%s root tag must be an object", version)
		}
		name, _ := tag["name"].(string)
		rootTags[name] = struct{}{}
	}
	for _, required := range []string{"OpenFGAService", "AuthZenService"} {
		if _, exists := rootTags[required]; !exists {
			return fmt.Errorf("%s is missing %s tag", version, required)
		}
	}

	authZenOperations := 0
	openFGAOperations := 0
	for _, entry := range operations {
		operationID := entry.operation["operationId"].(string)
		if strings.Contains(operationID, "UpdateStore") {
			return fmt.Errorf("%s must not expose the unimplemented UpdateStore operation", version)
		}
		tags, ok := entry.operation["tags"].([]any)
		if !ok {
			return fmt.Errorf("%s %s tags must be an array", version, operationID)
		}
		isAuthZen := false
		for _, tag := range tags {
			if tag == "AuthZenService" {
				isAuthZen = true
				break
			}
		}
		if isAuthZen {
			authZenOperations++
		} else {
			openFGAOperations++
		}
	}
	if authZenOperations == 0 {
		return fmt.Errorf("%s must contain AuthZen operations", version)
	}
	if openFGAOperations == 0 {
		return fmt.Errorf("%s must contain OpenFGA operations", version)
	}
	return nil
}

func validateContentFree204(operations map[string]operationInfo, version string) error {
	for _, entry := range operations {
		responses := entry.operation["responses"].(object)
		value, exists := responses["204"]
		if !exists {
			continue
		}
		response, ok := value.(object)
		if !ok {
			return fmt.Errorf("%s 204 response for %s must be an object", version, operationLabel(entry))
		}
		forbiddenKey := "content"
		if version == "OpenAPI v2" {
			forbiddenKey = "schema"
		}
		if _, exists := response[forbiddenKey]; exists {
			return fmt.Errorf(
				"%s 204 response for %s must not have %s",
				version,
				operationLabel(entry),
				forbiddenKey,
			)
		}
	}
	return nil
}

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

func writeAtomically(filename string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return fmt.Errorf("create OpenAPI v3 directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(filename), "."+filepath.Base(filename)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary OpenAPI v3 document: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)

	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return fmt.Errorf("set temporary OpenAPI v3 permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary OpenAPI v3 document: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary OpenAPI v3 document: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary OpenAPI v3 document: %w", err)
	}
	if err := os.Rename(temporaryName, filename); err != nil {
		return fmt.Errorf("replace OpenAPI v3 document: %w", err)
	}
	return nil
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

func operationLabel(entry operationInfo) string {
	return strings.ToUpper(entry.method) + " " + entry.apiPath
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
