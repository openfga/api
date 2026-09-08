package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
)

const (
	fixtureExpectedExamples      = 83
	fixtureExpectedOperations    = 24
	fixtureExpectedPaths         = 20
	fixtureExpectedRequestBodies = 16
	fixtureExpectedSchemas       = 114
)

func TestGenerateMatchesCommittedArtifactSemantics(t *testing.T) {
	source := readFixture(t, openAPIV2DefaultPath)
	generated, summary, err := generate(source)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	expected := readFixture(t, openAPIV3DefaultPath)
	generatedDocument, err := decodeObject(generated)
	if err != nil {
		t.Fatalf("decode generated document: %v", err)
	}
	expectedDocument, err := decodeObject(expected)
	if err != nil {
		t.Fatalf("decode committed document: %v", err)
	}
	if !equalJSON(expectedDocument, generatedDocument) {
		t.Fatal("generated document is not semantically identical to the committed artifact")
	}
	if summary != (generationSummary{
		examples:      fixtureExpectedExamples,
		operations:    fixtureExpectedOperations,
		paths:         fixtureExpectedPaths,
		refs:          expectedInternalRefs,
		requestBodies: fixtureExpectedRequestBodies,
		schemas:       fixtureExpectedSchemas,
	}) {
		t.Fatalf("unexpected generation summary: %+v", summary)
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	source := readFixture(t, openAPIV2DefaultPath)
	first, _, err := generate(source)
	if err != nil {
		t.Fatalf("first generation: %v", err)
	}
	second, _, err := generate(source)
	if err != nil {
		t.Fatalf("second generation: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("generation is not byte deterministic")
	}
}

func TestRunPreservesSourceAndWritesAtomically(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source.json")
	outputPath := filepath.Join(directory, "output.json")
	source := readFixture(t, openAPIV2DefaultPath)
	if err := os.WriteFile(sourcePath, source, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	if _, _, err := run(sourcePath, outputPath); err != nil {
		t.Fatalf("run: %v", err)
	}
	sourceAfter, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read source after run: %v", err)
	}
	if !bytes.Equal(source, sourceAfter) {
		t.Fatal("source changed during conversion")
	}
	if matches, err := filepath.Glob(filepath.Join(directory, ".output.json.*.tmp")); err != nil {
		t.Fatalf("glob temporary output: %v", err)
	} else if len(matches) != 0 {
		t.Fatalf("temporary output remains after atomic write: %v", matches)
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("unexpected output permissions: %o", info.Mode().Perm())
	}
}

func TestSourceMutationGuards(t *testing.T) {
	tests := []struct {
		name      string
		wantError string
		mutate    func(object)
	}{
		{
			name:      "root shape",
			wantError: `root "swagger"`,
			mutate: func(document object) {
				document["swagger"] = "1.0"
			},
		},
		{
			name:      "duplicate operation ID",
			wantError: "is duplicated",
			mutate: func(document object) {
				operation(document, "/stores", "post")["operationId"] = "ListStores"
			},
		},
		{
			name:      "external ref",
			wantError: "must be internal",
			mutate: func(document object) {
				schema(document, "ListUsersBody")["properties"].(object)["object"].(object)["$ref"] =
					"https://example.com/schema.json"
			},
		},
		{
			name:      "UpdateStore exposure",
			wantError: "must not expose the unimplemented UpdateStore",
			mutate: func(document object) {
				operation(document, "/stores/{store_id}", "get")["operationId"] = "UpdateStore"
			},
		},
		{
			name:      "contentful v2 204",
			wantError: "must not have schema",
			mutate: func(document object) {
				operation(document, "/stores/{store_id}", "delete")["responses"].(object)["204"].(object)["schema"] =
					object{"type": "object"}
			},
		},
		{
			name:      "ref sibling loss",
			wantError: "ref sibling count changed",
			mutate: func(document object) {
				delete(
					schema(document, "ActionSearchResponse")["properties"].(object)["page"].(object),
					"title",
				)
			},
		},
	}

	source := readFixture(t, openAPIV2DefaultPath)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, err := decodeObject(source)
			if err != nil {
				t.Fatalf("decode source: %v", err)
			}
			test.mutate(document)
			mutated, err := marshalJSON(document, true)
			if err != nil {
				t.Fatalf("encode mutation: %v", err)
			}
			_, _, err = generate(mutated)
			assertErrorContains(t, err, test.wantError)
		})
	}
}

func TestConvertedMutationGuards(t *testing.T) {
	source := readFixture(t, openAPIV2DefaultPath)
	originalV2, normalizedV2, v2Operations := prepareSource(t, source)
	generated, _, err := generate(source)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	tests := []struct {
		name      string
		wantError string
		mutate    func(object)
	}{
		{
			name:      "invented server",
			wantError: "invented servers",
			mutate: func(document object) {
				document["servers"] = []any{object{"url": "https://example.com"}}
			},
		},
		{
			name:      "invented security scheme",
			wantError: "invented security schemes",
			mutate: func(document object) {
				document["components"].(object)["securitySchemes"] = object{
					"bearer": object{"type": "http", "scheme": "bearer"},
				}
			},
		},
		{
			name:      "Swagger definition ref",
			wantError: "Swagger-only definition reference",
			mutate: func(document object) {
				schema(document, "ListUsersBody")["properties"].(object)["object"].(object)["allOf"].([]any)[0].(object)["$ref"] = "#/definitions/Object"
			},
		},
		{
			name:      "contentful v3 204",
			wantError: "must not have content",
			mutate: func(document object) {
				operation(document, "/stores/{store_id}", "delete")["responses"].(object)["204"].(object)["content"] =
					object{"application/json": object{}}
			},
		},
		{
			name:      "request body drift",
			wantError: "request body changed",
			mutate: func(document object) {
				operation(document, "/stores", "post")["requestBody"].(object)["required"] = false
			},
		},
		{
			name:      "response drift",
			wantError: "response 200 changed",
			mutate: func(document object) {
				operation(document, "/stores", "get")["responses"].(object)["200"].(object)["description"] = "changed"
			},
		},
		{
			name:      "example drift",
			wantError: "examples changed",
			mutate: func(document object) {
				delete(schema(document, "ConsistencyPreference"), "example")
			},
		},
		{
			name:      "converter extension",
			wantError: "converter-only extension",
			mutate: func(document object) {
				operation(document, "/stores", "post")["requestBody"].(object)["x-originalParamName"] = "body"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, err := decodeObject(generated)
			if err != nil {
				t.Fatalf("decode generated document: %v", err)
			}
			test.mutate(document)
			_, err = validateConverted(originalV2, normalizedV2, document, v2Operations)
			assertErrorContains(t, err, test.wantError)
		})
	}
}

func TestRawKinConversionCannotBypassRefSiblingGuard(t *testing.T) {
	source := readFixture(t, openAPIV2DefaultPath)
	originalV2, err := decodeObject(source)
	if err != nil {
		t.Fatalf("decode source: %v", err)
	}
	v2Operations, err := validateOpenAPIV2(originalV2)
	if err != nil {
		t.Fatalf("validate source: %v", err)
	}

	var typedV2 openapi2.T
	if err := json.Unmarshal(source, &typedV2); err != nil {
		t.Fatalf("decode typed source: %v", err)
	}
	typedV3, err := openapi2conv.ToV3(&typedV2)
	if err != nil {
		t.Fatalf("raw conversion: %v", err)
	}
	rawOutput, err := json.Marshal(typedV3)
	if err != nil {
		t.Fatalf("encode raw conversion: %v", err)
	}
	rawV3, err := decodeObject(rawOutput)
	if err != nil {
		t.Fatalf("decode raw conversion: %v", err)
	}
	if _, err := normalizeOutput(rawV3); err != nil {
		t.Fatalf("normalize raw output: %v", err)
	}
	_, err = validateConverted(originalV2, originalV2, rawV3, v2Operations)
	assertErrorContains(t, err, "examples changed")
}

func prepareSource(t *testing.T, source []byte) (object, object, map[string]operationInfo) {
	t.Helper()
	original, err := decodeObject(source)
	if err != nil {
		t.Fatalf("decode original source: %v", err)
	}

	operations, err := validateOpenAPIV2(original)
	if err != nil {
		t.Fatalf("validate original source: %v", err)
	}
	normalized, err := decodeObject(source)
	if err != nil {
		t.Fatalf("decode normalized source: %v", err)
	}
	value, count, err := normalizeRefSiblings(normalized)
	if err != nil {
		t.Fatalf("normalize ref siblings: %v", err)
	}
	if count != expectedRefSiblings {
		t.Fatalf("unexpected ref sibling count: %d", count)
	}
	return original, value.(object), operations
}

func readFixture(t *testing.T, filename string) []byte {
	t.Helper()
	value, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	return value
}

func assertErrorContains(t *testing.T, err error, expected string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q", expected)
	}
	if !strings.Contains(err.Error(), expected) {
		t.Fatalf("expected error containing %q, got %q", expected, err)
	}
}

func operation(document object, apiPath, method string) object {
	return document["paths"].(object)[apiPath].(object)[method].(object)
}

func schema(document object, name string) object {
	if definitions, exists := document["definitions"].(object); exists {
		return definitions[name].(object)
	}
	return document["components"].(object)["schemas"].(object)[name].(object)
}
