#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest="$root/scripts/proto-files.txt"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cp -a "$root/proto/." "$work/"

# The recovered legacy MessageSet descriptor is intentionally empty in agy,
# although a few unrelated internal schemas still refer to its historical Go
# type. Drop only those generation-blocking fields in the temporary copy. The
# byte-exact recovered schemas under proto/ are never edited.
sed -i '/\.proto2\.bridge\.MessageSet scope = 1;/d' \
  "$work/security/credentials/proto/data_access_token_scope.proto"
sed -i '/\.proto2\.bridge\.MessageSet attribute = 2;/d' \
  "$work/security/credentials/proto/authenticator.proto"

set_go_package() {
  local file="$work/$1"
  local value="$2"
  if grep -q '^option go_package = ' "$file"; then
    sed -i "s#^option go_package = .*#option go_package = \"$value\";#" "$file"
  else
    sed -i "/^package /a option go_package = \"$value\";" "$file"
  fi
}

# The internal descriptors do not carry Go package metadata. Derive a stable
# local package from each protobuf package. Existing public Go mappings stay in
# place so their canonical modules satisfy those imports.
while IFS= read -r relative; do
  [[ -z "$relative" ]] && continue
  file="$work/$relative"
  current="$(sed -n 's/^option go_package = "\([^"]*\)";.*/\1/p' "$file" | head -n1)"
  case "$current" in
    google.golang.org/*|cloud.google.com/*) continue ;;
  esac

  package="$(sed -n 's/^package \([^;]*\);/\1/p' "$file" | head -n1)"
  path="${package//./\/}"
  name="${package##*.}"
  set_go_package "$relative" "antigravity-go-proxy/gen/$path;$name"
done < "$manifest"

# Three public-looking internal annotations are absent from genproto, and two
# shared v1internal schemas must live outside Go's reserved /internal/ path.
set_go_package google/api/inclusion.proto \
  'antigravity-go-proxy/gen/google/api/inclusion;inclusion'
set_go_package google/api/auditing.proto \
  'antigravity-go-proxy/gen/google/api/auditing;auditing'
set_go_package apps/framework/data/service_annotations.proto \
  'antigravity-go-proxy/gen/apps/framework/data;data'
for relative in \
  google/internal/cloud/code/v1internal/cloudcode.proto \
  google/internal/cloud/code/v1internal/entitlement.proto \
  google/internal/cloud/code/v1internal/metrics.proto \
  google/internal/cloud/code/v1internal/model_configs.proto \
  google/internal/cloud/code/v1internal/prediction_service.proto \
  google/internal/cloud/code/v1internal/remote_context.proto; do
  set_go_package "$relative" \
    'antigravity-go-proxy/gen/v1internal;v1internal'
done
set_go_package google/internal/cloud/code/v1internal/credits.proto \
  'antigravity-go-proxy/gen/v1internaltypes;v1internaltypes'
set_go_package google/internal/cloud/code/v1internal/onboarding.proto \
  'antigravity-go-proxy/gen/v1internaltypes;v1internaltypes'

# These protobuf packages refer to each other. Keeping the generated files in
# one Go package removes an artifact-only import cycle without changing wire
# names, field numbers, or the recovered descriptors.
set_go_package security/data_access/proto/standard_dat_scope.proto \
  'antigravity-go-proxy/gen/security/credentials;credentials'

generate=()
while IFS= read -r relative; do
  [[ -z "$relative" ]] && continue
  if grep -q '^option go_package = "antigravity-go-proxy/' "$work/$relative"; then
    generate+=("$relative")
  fi
done < "$manifest"

export PATH="$(go env GOPATH)/bin:$PATH"
rm -rf "$root/gen"
protoc -I "$work" \
  --go_out="$root" --go_opt=module=antigravity-go-proxy \
  --go-grpc_out="$root" --go-grpc_opt=module=antigravity-go-proxy \
  "${generate[@]}"
gofmt -w "$root/gen"
