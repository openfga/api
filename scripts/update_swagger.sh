#!/bin/sh

# This script is used to cleanup swagger file
if [ "$#" -ne 1 ]; then
  echo "Usage: update_swagger.sh <filename>" >&2
  exit 1
fi

filename=$1
tmp_filename=$filename.tmp

# We also need to cleanup response code that are obsolete because
# either i) we override the error code or ii) we override success case where we respond with 201/204
cat ${filename} | \
  jq \
    'del(.paths[][]."responses"|select(has("400"))."default" ) | del(.paths[][]."responses"|select(has("201"))."200") | del(.paths[][]."responses"|select(has("204"))."200")' > ${tmp_filename}
mv ${tmp_filename} ${filename}

# Finally, for 204, there should be no schema
cat ${filename} | \
  jq \
    'del(.paths[][]."responses"."204"."schema")' > ${tmp_filename}
mv ${tmp_filename} ${filename}
