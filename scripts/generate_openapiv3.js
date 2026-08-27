#!/usr/bin/env node

"use strict";

const crypto = require("crypto");
const fs = require("fs/promises");
const path = require("path");
const converter = require("swagger2openapi");
const converterPackage = require("swagger2openapi/package.json");

const HTTP_METHODS = new Set([
  "delete",
  "get",
  "head",
  "options",
  "patch",
  "post",
  "put",
  "trace",
]);
const REQUIRED_CONVERTER_VERSION = "7.0.8";
const ROOT = path.resolve(__dirname, "..");
const OPENAPI_V2_PATH = path.join(
  ROOT,
  "docs",
  "openapiv2",
  "apidocs.swagger.json",
);
const OPENAPI_V3_PATH = path.join(
  ROOT,
  "docs",
  "openapiv3",
  "apidocs.openapi.json",
);

function invariant(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}

function isObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function requireObject(value, label) {
  invariant(isObject(value), `${label} must be an object`);
}

function hasOwn(value, key) {
  return Object.prototype.hasOwnProperty.call(value, key);
}

function hash(value) {
  return crypto.createHash("sha256").update(value).digest("hex");
}

function canonicalJson(value) {
  if (Array.isArray(value)) {
    return `[${value.map(canonicalJson).join(",")}]`;
  }
  if (isObject(value)) {
    return `{${Object.keys(value)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${canonicalJson(value[key])}`)
      .join(",")}}`;
  }
  return JSON.stringify(value);
}

function walk(value, visit, location = "#") {
  if (Array.isArray(value)) {
    value.forEach((item, index) => walk(item, visit, `${location}/${index}`));
    return;
  }
  if (!isObject(value)) {
    return;
  }

  visit(value, location);
  for (const [key, child] of Object.entries(value)) {
    const escapedKey = key.replaceAll("~", "~0").replaceAll("/", "~1");
    walk(child, visit, `${location}/${escapedKey}`);
  }
}

function validateRootShapeV2(document) {
  requireObject(document, "OpenAPI v2 document");
  invariant(document.swagger === "2.0", 'OpenAPI v2 root "swagger" must be "2.0"');
  invariant(!hasOwn(document, "openapi"), 'OpenAPI v2 root must not contain "openapi"');
  requireObject(document.info, "OpenAPI v2 info");
  requireObject(document.paths, "OpenAPI v2 paths");
  requireObject(document.definitions, "OpenAPI v2 definitions");
  invariant(Array.isArray(document.tags), "OpenAPI v2 tags must be an array");
}

function validateRootShapeV3(document) {
  requireObject(document, "OpenAPI v3 document");
  invariant(document.openapi === "3.0.3", 'OpenAPI v3 root "openapi" must be "3.0.3"');
  requireObject(document.info, "OpenAPI v3 info");
  requireObject(document.paths, "OpenAPI v3 paths");
  requireObject(document.components, "OpenAPI v3 components");
  requireObject(document.components.schemas, "OpenAPI v3 component schemas");
  invariant(Array.isArray(document.tags), "OpenAPI v3 tags must be an array");

  for (const key of [
    "swagger",
    "definitions",
    "parameters",
    "responses",
    "securityDefinitions",
    "schemes",
    "consumes",
    "produces",
  ]) {
    invariant(!hasOwn(document, key), `OpenAPI v3 root contains Swagger-only key "${key}"`);
  }
}

function collectParameters(pathItem, operation) {
  return [...(pathItem.parameters || []), ...(operation.parameters || [])];
}

function collectOperations(document, version) {
  const operations = new Map();
  const operationIds = new Map();

  for (const [apiPath, pathItem] of Object.entries(document.paths)) {
    invariant(apiPath.startsWith("/"), `${version} path "${apiPath}" must start with "/"`);
    requireObject(pathItem, `${version} path item ${apiPath}`);

    for (const [method, operation] of Object.entries(pathItem)) {
      if (!HTTP_METHODS.has(method)) {
        continue;
      }

      requireObject(operation, `${version} operation ${method.toUpperCase()} ${apiPath}`);
      invariant(
        typeof operation.operationId === "string" && operation.operationId.length > 0,
        `${version} operation ${method.toUpperCase()} ${apiPath} must have an operationId`,
      );
      invariant(
        !operationIds.has(operation.operationId),
        `${version} operationId "${operation.operationId}" is duplicated`,
      );
      operationIds.set(operation.operationId, `${method.toUpperCase()} ${apiPath}`);
      requireObject(
        operation.responses,
        `${version} responses for ${method.toUpperCase()} ${apiPath}`,
      );

      const responseCodes = Object.keys(operation.responses)
        .filter((code) => !code.startsWith("x-"))
        .sort();
      invariant(
        responseCodes.length > 0,
        `${version} operation ${method.toUpperCase()} ${apiPath} must define responses`,
      );

      operations.set(`${apiPath}\t${method}`, {
        apiPath,
        method,
        operation,
        pathItem,
        responseCodes,
      });
    }
  }

  return operations;
}

function validateInternalRefs(document, version) {
  let count = 0;

  walk(document, (value, location) => {
    if (!hasOwn(value, "$ref")) {
      return;
    }

    count += 1;
    const ref = value.$ref;
    invariant(typeof ref === "string", `${version} $ref at ${location} must be a string`);
    invariant(
      ref === "#" || ref.startsWith("#/"),
      `${version} $ref at ${location} must be internal: ${ref}`,
    );

    let target = document;
    if (ref !== "#") {
      const tokens = ref
        .slice(2)
        .split("/")
        .map((token) =>
          decodeURIComponent(token).replaceAll("~1", "/").replaceAll("~0", "~"),
        );
      for (const token of tokens) {
        invariant(
          isObject(target) || Array.isArray(target),
          `${version} $ref at ${location} does not resolve: ${ref}`,
        );
        invariant(
          hasOwn(target, token),
          `${version} $ref at ${location} does not resolve: ${ref}`,
        );
        target = target[token];
      }
    }
  });

  return count;
}

function collectExamples(document) {
  const examples = new Map();

  walk(document, (value) => {
    if (!hasOwn(value, "example")) {
      return;
    }
    const example = canonicalJson(value.example);
    examples.set(example, (examples.get(example) || 0) + 1);
  });

  return examples;
}

function validateServiceCoverage(document, operations, version) {
  const rootTagNames = new Set(document.tags.map((tag) => tag.name));
  invariant(rootTagNames.has("OpenFGAService"), `${version} is missing OpenFGAService tag`);
  invariant(rootTagNames.has("AuthZenService"), `${version} is missing AuthZenService tag`);

  let authZenOperations = 0;
  let openFgaOperations = 0;
  for (const { operation } of operations.values()) {
    invariant(
      Array.isArray(operation.tags),
      `${version} ${operation.operationId} tags must be an array`,
    );
    if (operation.tags.includes("AuthZenService")) {
      authZenOperations += 1;
    } else {
      openFgaOperations += 1;
    }
    invariant(
      !operation.operationId.includes("UpdateStore"),
      `${version} must not expose the unimplemented UpdateStore operation`,
    );
  }

  invariant(authZenOperations > 0, `${version} must contain AuthZen operations`);
  invariant(openFgaOperations > 0, `${version} must contain OpenFGA operations`);
}

function validateContentFree204(operations, version) {
  for (const { apiPath, method, operation } of operations.values()) {
    const response = operation.responses["204"];
    if (!response) {
      continue;
    }
    requireObject(
      response,
      `${version} 204 response for ${method.toUpperCase()} ${apiPath}`,
    );
    if (version === "OpenAPI v2") {
      invariant(
        !hasOwn(response, "schema"),
        `${version} 204 response for ${method.toUpperCase()} ${apiPath} must not have a schema`,
      );
    } else {
      invariant(
        !hasOwn(response, "content"),
        `${version} 204 response for ${method.toUpperCase()} ${apiPath} must not have content`,
      );
    }
  }
}

function validateNoSwaggerOnlyV3(document, operations) {
  for (const { apiPath, method, operation, pathItem } of operations.values()) {
    for (const key of ["consumes", "produces", "schemes"]) {
      invariant(
        !hasOwn(operation, key),
        `OpenAPI v3 operation ${method.toUpperCase()} ${apiPath} contains Swagger-only key "${key}"`,
      );
    }

    for (const parameter of collectParameters(pathItem, operation)) {
      requireObject(
        parameter,
        `OpenAPI v3 parameter for ${method.toUpperCase()} ${apiPath}`,
      );
      invariant(
        parameter.in !== "body" && parameter.in !== "formData",
        `OpenAPI v3 operation ${method.toUpperCase()} ${apiPath} contains a Swagger-only ${parameter.in} parameter`,
      );
      for (const key of ["type", "format", "items", "collectionFormat"]) {
        invariant(
          !hasOwn(parameter, key),
          `OpenAPI v3 parameter for ${method.toUpperCase()} ${apiPath} contains Swagger-only key "${key}"`,
        );
      }
    }

    for (const [code, response] of Object.entries(operation.responses)) {
      if (code.startsWith("x-")) {
        continue;
      }
      requireObject(
        response,
        `OpenAPI v3 response ${code} for ${method.toUpperCase()} ${apiPath}`,
      );
      for (const key of ["schema", "examples"]) {
        invariant(
          !hasOwn(response, key),
          `OpenAPI v3 response ${code} for ${method.toUpperCase()} ${apiPath} contains Swagger-only key "${key}"`,
        );
      }
    }
  }

  walk(document, (value, location) => {
    for (const key of Object.keys(value)) {
      invariant(
        !key.toLowerCase().startsWith("x-mintlify"),
        `OpenAPI v3 contains Mintlify-specific extension "${key}" at ${location}`,
      );
    }
    if (hasOwn(value, "$ref")) {
      invariant(
        !value.$ref.startsWith("#/definitions/"),
        `OpenAPI v3 contains a Swagger-only definition reference at ${location}`,
      );
    }
  });
}

function validateParity(v2, v3, v2Operations, v3Operations) {
  invariant(
    canonicalJson(Object.keys(v2.paths).sort()) ===
      canonicalJson(Object.keys(v3.paths).sort()),
    "API paths changed during OpenAPI v3 conversion",
  );
  invariant(
    v2Operations.size === v3Operations.size,
    `operation count changed from ${v2Operations.size} to ${v3Operations.size}`,
  );

  for (const [key, v2Entry] of v2Operations) {
    const v3Entry = v3Operations.get(key);
    invariant(
      v3Entry,
      `OpenAPI v3 is missing ${v2Entry.method.toUpperCase()} ${v2Entry.apiPath}`,
    );

    for (const property of ["operationId", "summary", "description", "tags"]) {
      invariant(
        canonicalJson(v2Entry.operation[property]) ===
          canonicalJson(v3Entry.operation[property]),
        `${property} changed for ${v2Entry.method.toUpperCase()} ${v2Entry.apiPath}`,
      );
    }
    invariant(
      canonicalJson(v2Entry.responseCodes) === canonicalJson(v3Entry.responseCodes),
      `response codes changed for ${v2Entry.method.toUpperCase()} ${v2Entry.apiPath}`,
    );
    for (const code of v2Entry.responseCodes) {
      invariant(
        v2Entry.operation.responses[code].description ===
          v3Entry.operation.responses[code].description,
        `response ${code} description changed for ${v2Entry.method.toUpperCase()} ${v2Entry.apiPath}`,
      );
    }
  }

  const v2Schemas = Object.keys(v2.definitions).sort();
  const v3Schemas = Object.keys(v3.components.schemas).sort();
  invariant(
    canonicalJson(v2Schemas) === canonicalJson(v3Schemas),
    "component schema names changed during OpenAPI v3 conversion",
  );
  invariant(
    canonicalJson(v2.tags) === canonicalJson(v3.tags),
    "root tags changed during conversion",
  );

  const v2Examples = collectExamples(v2);
  const v3Examples = collectExamples(v3);
  invariant(
    canonicalJson(Object.fromEntries(v2Examples)) ===
      canonicalJson(Object.fromEntries(v3Examples)),
    "examples changed during OpenAPI v3 conversion",
  );

  if (!hasOwn(v2, "host") && !hasOwn(v2, "basePath")) {
    invariant(!hasOwn(v3, "servers"), "OpenAPI v3 conversion invented servers");
  }
  if (!hasOwn(v2, "securityDefinitions")) {
    invariant(
      !hasOwn(v3.components, "securitySchemes"),
      "OpenAPI v3 conversion invented security schemes",
    );
  }
  if (!hasOwn(v2, "security")) {
    invariant(!hasOwn(v3, "security"), "OpenAPI v3 conversion invented root security policy");
  }
}

async function writeAtomically(filename, contents) {
  await fs.mkdir(path.dirname(filename), { recursive: true });
  const temporaryFilename = path.join(
    path.dirname(filename),
    `.${path.basename(filename)}.${process.pid}.tmp`,
  );

  try {
    await fs.writeFile(temporaryFilename, contents, { flag: "wx" });
    await fs.rename(temporaryFilename, filename);
  } finally {
    await fs.rm(temporaryFilename, { force: true });
  }
}

async function main() {
  invariant(
    converterPackage.version === REQUIRED_CONVERTER_VERSION,
    `swagger2openapi ${REQUIRED_CONVERTER_VERSION} is required; found ${converterPackage.version}`,
  );

  const sourceBytes = await fs.readFile(OPENAPI_V2_PATH);
  const sourceHash = hash(sourceBytes);
  const openapiV2 = JSON.parse(sourceBytes.toString("utf8"));
  validateRootShapeV2(openapiV2);
  const v2Operations = collectOperations(openapiV2, "OpenAPI v2");
  validateServiceCoverage(openapiV2, v2Operations, "OpenAPI v2");
  validateContentFree204(v2Operations, "OpenAPI v2");
  validateInternalRefs(openapiV2, "OpenAPI v2");

  const conversion = await converter.convertObj(openapiV2, {
    refSiblings: "allOf",
    targetVersion: "3.0.3",
  });
  invariant(conversion.patches === 0, "swagger2openapi unexpectedly patched the v2 input");
  invariant(
    !conversion.warnings || conversion.warnings.length === 0,
    "swagger2openapi produced conversion warnings",
  );

  const openapiV3 = conversion.openapi;
  validateRootShapeV3(openapiV3);
  const v3Operations = collectOperations(openapiV3, "OpenAPI v3");
  validateNoSwaggerOnlyV3(openapiV3, v3Operations);
  validateServiceCoverage(openapiV3, v3Operations, "OpenAPI v3");
  validateContentFree204(v3Operations, "OpenAPI v3");
  validateInternalRefs(openapiV3, "OpenAPI v3");
  validateParity(openapiV2, openapiV3, v2Operations, v3Operations);

  const sourceBytesAfterConversion = await fs.readFile(OPENAPI_V2_PATH);
  invariant(
    sourceHash === hash(sourceBytesAfterConversion) &&
      sourceBytes.equals(sourceBytesAfterConversion),
    "OpenAPI v2 artifact changed during OpenAPI v3 conversion",
  );

  await writeAtomically(OPENAPI_V3_PATH, `${JSON.stringify(openapiV3, null, 2)}\n`);

  console.log(
    `Generated OpenAPI 3.0.3: ${Object.keys(openapiV3.paths).length} paths, ` +
      `${v3Operations.size} operations, ${Object.keys(openapiV3.components.schemas).length} schemas, ` +
      `${[...collectExamples(openapiV3).values()].reduce((sum, count) => sum + count, 0)} examples`,
  );
  console.log(`OpenAPI v2 SHA-256 unchanged: ${sourceHash}`);
}

main().catch((error) => {
  console.error(error.stack || error.message);
  process.exitCode = 1;
});
