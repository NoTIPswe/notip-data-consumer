#!/usr/bin/env bash
# Fetch a specific AsyncAPI spec from notip-infra at a given tag, filter it to the
# current service's channels/operations, generate Go models, and commit.
#
# Usage:
#   ./scripts/generate-asyncapi.sh --tag v1.2.3 --file my-events.yaml
#   ./scripts/generate-asyncapi.sh --tag v1.2.3 --file my-events.yaml --service my-service
#
# Arguments:
#   --tag      Git tag or branch in notip-infra (required)
#   --file     Filename inside api-contracts/asyncapi/ in notip-infra (required)
#   --service  Service tag to filter by (default: data-consumer)
#
# Requirements: gh CLI, node (for @asyncapi/cli), git
set -euo pipefail

REPO="notipswe/notip-infra"
REMOTE_BASE="api-contracts/asyncapi"
LOCAL_DIR="api-contracts/asyncapi"
OUT_DIR="internal/generated/asyncapi"

SERVICE="data-consumer"
TAG=""
FILE=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --tag)     TAG="$2";     shift 2 ;;
    --file)    FILE="$2";    shift 2 ;;
    --service) SERVICE="$2"; shift 2 ;;
    *) echo "Unknown argument: $1"; exit 1 ;;
  esac
done

[[ -z "$TAG"  ]] && { echo "Error: --tag is required";  exit 1; }
[[ -z "$FILE" ]] && { echo "Error: --file is required"; exit 1; }

mkdir -p "$LOCAL_DIR"

# ---------------------------------------------------------------------------
# 1. Fetch the full spec from notip-infra (source of truth)
# ---------------------------------------------------------------------------
echo "Fetching ${FILE} from ${REPO}@${TAG}..."
gh api "repos/${REPO}/contents/${REMOTE_BASE}/${FILE}?ref=${TAG}" \
  --jq '.content' \
  | tr -d '\n' \
  | base64 -d > "${LOCAL_DIR}/${FILE}"
echo "  Saved → ${LOCAL_DIR}/${FILE}"

# ---------------------------------------------------------------------------
# 2. Filter the spec to only entries tagged with the service name
# ---------------------------------------------------------------------------
NAME="${FILE%.*}"
FILTERED_TMP=$(mktemp /tmp/asyncapi-filtered-XXXXXX.yaml)
trap 'rm -f "$FILTERED_TMP"' EXIT

echo "Filtering spec for service '${SERVICE}'..."
node scripts/filter-asyncapi.mjs \
  --input  "${LOCAL_DIR}/${FILE}" \
  --output "${FILTERED_TMP}" \
  --service "${SERVICE}"

# ---------------------------------------------------------------------------
# 3. Generate Go models from the filtered spec
# ---------------------------------------------------------------------------
OUTDIR="${OUT_DIR}/${NAME}"
mkdir -p "${OUTDIR}"

echo "Generating Go models → ${OUTDIR}/"
npx @asyncapi/cli generate models go "${FILTERED_TMP}" --output "${OUTDIR}"

# ---------------------------------------------------------------------------
# 4. Commit the raw spec + generated models
# ---------------------------------------------------------------------------
echo ""
echo "Committing..."
git add "${LOCAL_DIR}/${FILE}" "${OUTDIR}"
git commit "${LOCAL_DIR}/${FILE}" "${OUTDIR}" -m "chore(contracts): fetch asyncapi ${FILE} from notip-infra@${TAG} (service: ${SERVICE})"

echo "Done."
