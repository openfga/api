package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
)

const (
	// These counts are reviewed-change sentinels: legitimate generator changes must update them deliberately.
	expectedInternalRefs   = 330
	expectedRefSiblings    = 43
	openAPIV2DefaultPath   = "../../docs/openapiv2/apidocs.swagger.json"
	openAPIV3DefaultPath   = "../../docs/openapiv3/apidocs.openapi.json"
	openAPIV3TargetVersion = "3.0.3"
)

type generationSummary struct {
	examples      int
	operations    int
	paths         int
	refs          int
	requestBodies int
	schemas       int
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
