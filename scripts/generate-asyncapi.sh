#!/usr/bin/env bash
# Fetch a specific AsyncAPI spec from a producer repository at a given tag.
#
# Usage:
#   make fetch-contracts REPO=notipswe/notip-infra TAG=main FILE=nats-contracts.yaml
#
# Arguments:
#   --repo     Source GitHub repository (required)
#   --tag      Git tag or branch in the source repo (required)
#   --file     Filename inside api-contracts/asyncapi/ in the source repo (required)
#   --service  Service tag to filter by (default: data-consumer)
set -euo pipefail

REMOTE_BASE="api-contracts/asyncapi"
LOCAL_DIR="api-contracts/asyncapi"
# IN GO: I modelli generati vanno nella cartella internal
OUT_DIR="internal/generated/asyncapi"

REPO=""
SERVICE="data-consumer"
TAG=""
FILE=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --repo)    REPO="$2";    shift 2 ;;
    --tag)     TAG="$2";     shift 2 ;;
    --file)    FILE="$2";    shift 2 ;;
    --service) SERVICE="$2"; shift 2 ;;
    *) echo "Unknown argument: $1"; exit 1 ;;
  esac
done

[[ -z "$REPO" ]] && { echo "Error: --repo is required"; exit 1; }
[[ -z "$TAG"  ]] && { echo "Error: --tag is required";  exit 1; }
[[ -z "$FILE" ]] && { echo "Error: --file is required"; exit 1; }

mkdir -p "$LOCAL_DIR"

# ---------------------------------------------------------------------------
# 1. Fetch the full spec from the producer repo
# ---------------------------------------------------------------------------
echo "Fetching ${FILE} from ${REPO}@${TAG}..."
gh api \
  -H "Accept: application/vnd.github.raw" \
  "repos/${REPO}/contents/${REMOTE_BASE}/${FILE}?ref=${TAG}" \
  > "${LOCAL_DIR}/${FILE}"

if [[ ! -s "${LOCAL_DIR}/${FILE}" ]]; then
  echo "Error: fetched file is empty (${LOCAL_DIR}/${FILE}). Check --repo/--tag/--file and repository access."
  exit 1
fi

if ! grep -Eq '^[[:space:]]*asyncapi[[:space:]]*:|"asyncapi"[[:space:]]*:' "${LOCAL_DIR}/${FILE}"; then
  echo "Error: fetched file does not look like an AsyncAPI spec (missing top-level 'asyncapi' field)."
  exit 1
fi
echo "  Saved → ${LOCAL_DIR}/${FILE}"

# # ---------------------------------------------------------------------------
# # 2. Filter the spec to only entries tagged with the service name
# # ---------------------------------------------------------------------------
# NAME="${FILE%.*}"
# FILTERED_TMP=$(mktemp /tmp/asyncapi-filtered-XXXXXX.yaml)
# trap 'rm -f "$FILTERED_TMP"' EXIT

# echo "Filtering spec for service '${SERVICE}'..."
# node scripts/filter-asyncapi.mjs \
#   --input  "${LOCAL_DIR}/${FILE}" \
#   --output "${FILTERED_TMP}" \
#   --service "${SERVICE}"

# # ---------------------------------------------------------------------------
# # 3. Generate Go models from the filtered spec
# # ---------------------------------------------------------------------------
# OUTDIR="${OUT_DIR}/${NAME}"
# mkdir -p "${OUTDIR}"

# echo "Generating Go models → ${OUTDIR}/"
# # IN GO: cambiamo il target da typescript a go
# npx @asyncapi/cli generate models golang "${FILTERED_TMP}" --output "${OUTDIR}" --packageName natscontracts

# # Formattiamo il codice generato per allinearlo agli standard Go
# go fmt "./${OUTDIR}/..."

# echo "Done! Generated models in ${OUTDIR}"