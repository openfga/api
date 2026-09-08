#!/bin/sh

# This script is used to cleanup swagger file and convert to OpenAPI v3
if [ "$#" -ne 1 ]; then
  echo "Usage: update_swagger.sh <filename>" >&2
  exit 1
fi

filename=$1
tmp_filename=$filename.tmp
openapi_v3_dir="docs/openapiv3"
openapi_v3_filename="${openapi_v3_dir}/apidocs.swagger.json"

# We also need to cleanup response code that are obsolete because
# either i) we override the error code or ii) we override success case where we respond with 201/204
cat ${filename} |
  jq \
    'del(.paths[][]."responses"|select(has("400"))."default" ) | del(.paths[][]."responses"|select(has("201"))."200") | del(.paths[][]."responses"|select(has("204"))."200")' >${tmp_filename}
mv ${tmp_filename} ${filename}

# Add an example value to the ConsistencyPreference to override the default of UNSPECIFIED being shown in the docs
cat ${filename} |
  jq \
    '.definitions.ConsistencyPreference.example = "MINIMIZE_LATENCY"' >${tmp_filename}
mv ${tmp_filename} ${filename}

# Finally, for 204, there should be no schema
cat ${filename} |
  jq \
    'del(.paths[][]."responses"."204"."schema")' >${tmp_filename}
mv ${tmp_filename} ${filename}

# Convert OpenAPI v2 to v3
if command -v api-spec-converter >/dev/null; then
  # Check if the directory exists, if not create it
  if [ ! -d "${openapi_v3_dir}" ]; then
    if ! mkdir -p "${openapi_v3_dir}"; then
      echo "Failed to create directory: ${openapi_v3_dir}" >&2
      exit 1
    fi
    echo "Created directory: ${openapi_v3_dir}"
  fi

  if api-spec-converter --from=swagger_2 --to=openapi_3 --syntax=json ${filename} >${openapi_v3_filename}; then
    echo "Converted ${filename} to OpenAPI v3 and saved as ${openapi_v3_filename}"
  else
    echo "Failed to convert ${filename} to OpenAPI v3" >&2
    exit 1
  fi
else
  echo "api-spec-converter not found. Please install it to convert to OpenAPI v3." >&2
  echo "You can install it using: npm install -g api-spec-converter" >&2
  exit 1
fi
