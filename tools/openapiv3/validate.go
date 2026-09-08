package main

import (
	"errors"
	"fmt"
	"strings"
)

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
