#!/usr/bin/env bash
set -Eeuo pipefail

# Sequential destructive-safe smoke test for every AI Assistants Platform endpoint.
# It creates uniquely named temporary records and removes/deactivates them on exit.
# Requirements: bash, curl, jq; running backend; valid OPENAI_API_KEY for embeddings/chat.

BASE_URL="${BASE_URL:-http://localhost:18473}"
API="$BASE_URL/api/v1"
OWNER_EMAIL="${OWNER_EMAIL:-}"
OWNER_PASSWORD="${OWNER_PASSWORD:-}"
TEST_CHAT_MODEL="${TEST_CHAT_MODEL:-gpt-4o-mini}"
WAIT_ATTEMPTS="${WAIT_ATTEMPTS:-40}"
INSECURE_TLS="${INSECURE_TLS:-false}"
CURL_RESOLVE="${CURL_RESOLVE:-}"
KEEP_TEST_DATA="${KEEP_TEST_DATA:-false}"
RUN_ID="$(date +%s)-$RANDOM"
TMP_DIR="$(mktemp -d)"

for command in curl jq; do
  command -v "$command" >/dev/null || { echo "Missing dependency: $command" >&2; exit 1; }
done

CURL_TLS_ARGS=()
if [[ "$INSECURE_TLS" == "true" ]]; then
  CURL_TLS_ARGS=(-k)
  echo 'WARNING: TLS certificate verification is disabled for this test run.' >&2
fi
[[ -n "$CURL_RESOLVE" ]] && CURL_TLS_ARGS+=(--resolve "$CURL_RESOLVE")
curl() { command curl "${CURL_TLS_ARGS[@]}" "$@"; }

cleanup() {
  local token
  set +e
  if [[ "$KEEP_TEST_DATA" != "true" && -n "${OWNER_ACCESS:-}" ]]; then
    [[ -n "${FEEDBACK_ID:-}" ]] && curl -sS -o /dev/null -X DELETE -H "Authorization: Bearer $OWNER_ACCESS" "$API/feedback/$FEEDBACK_ID"
    [[ -n "${TRAINING_ENTRY_ID:-}" ]] && curl -sS -o /dev/null -X DELETE -H "Authorization: Bearer $OWNER_ACCESS" "$API/training-entries/$TRAINING_ENTRY_ID"
    [[ -n "${CONVERSATION_ID:-}" ]] && curl -sS -o /dev/null -X DELETE -H "Authorization: Bearer $OWNER_ACCESS" "$API/conversations/$CONVERSATION_ID"
    if [[ -n "${ASSISTANT_ID:-}" ]]; then
      curl -sS -o /dev/null -X DELETE -H "Authorization: Bearer $OWNER_ACCESS" "$API/assistants/$ASSISTANT_ID/publication"
      [[ -n "${EMPLOYEE_ID:-}" ]] && curl -sS -o /dev/null -X DELETE -H "Authorization: Bearer $OWNER_ACCESS" "$API/assistants/$ASSISTANT_ID/users/$EMPLOYEE_ID"
      [[ -n "${KB_ID:-}" ]] && curl -sS -o /dev/null -X DELETE -H "Authorization: Bearer $OWNER_ACCESS" "$API/assistants/$ASSISTANT_ID/knowledge-bases/$KB_ID"
    fi
    [[ -n "${UPLOAD_DOCUMENT_ID:-}" ]] && curl -sS -o /dev/null -X DELETE -H "Authorization: Bearer $OWNER_ACCESS" "$API/documents/$UPLOAD_DOCUMENT_ID"
    [[ -n "${TEXT_DOCUMENT_ID:-}" ]] && curl -sS -o /dev/null -X DELETE -H "Authorization: Bearer $OWNER_ACCESS" "$API/documents/$TEXT_DOCUMENT_ID"
    [[ -n "${ASSISTANT_ID:-}" ]] && curl -sS -o /dev/null -X DELETE -H "Authorization: Bearer $OWNER_ACCESS" "$API/assistants/$ASSISTANT_ID"
    [[ -n "${KB_ID:-}" ]] && curl -sS -o /dev/null -X DELETE -H "Authorization: Bearer $OWNER_ACCESS" "$API/knowledge-bases/$KB_ID"
    [[ -n "${EMPLOYEE_ID:-}" ]] && curl -sS -o /dev/null -X DELETE -H "Authorization: Bearer $OWNER_ACCESS" "$API/users/$EMPLOYEE_ID"
  fi
  if command -v curl >/dev/null 2>&1; then
    for token in "${EMPLOYEE_REFRESH:-}" "${OWNER_REFRESH:-}"; do
      if [[ -n "$token" ]]; then
        curl -sS -o /dev/null -X POST -H 'Content-Type: application/json' \
          --data "{\"refresh_token\":\"$token\"}" "$API/auth/logout" || true
      fi
    done
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

if command -v docker >/dev/null 2>&1 && docker inspect ai-platform-postgres >/dev/null 2>&1; then
  DB_USER="$(docker exec ai-platform-postgres printenv POSTGRES_USER)"
  DB_NAME="$(docker exec ai-platform-postgres printenv POSTGRES_DB)"
  DETECTED_OWNER_EMAIL="$(docker exec ai-platform-postgres psql -U "$DB_USER" -d "$DB_NAME" -Atc \
    "SELECT u.email FROM users u JOIN organization_users ou ON ou.user_id=u.id WHERE ou.role='owner' ORDER BY ou.created_at LIMIT 1;")"
  if [[ -n "$DETECTED_OWNER_EMAIL" ]]; then
    OWNER_EMAIL="$DETECTED_OWNER_EMAIL"
    printf 'Detected database owner email: %s\n' "$OWNER_EMAIL"
  fi
fi

if [[ "${NONINTERACTIVE:-false}" != "true" ]]; then
  if [[ -z "$OWNER_EMAIL" ]]; then
    read -r -p 'Database owner email: ' OWNER_EMAIL
  fi
  read -r -s -p 'Database owner password: ' OWNER_PASSWORD
  printf '\n'
fi
OWNER_EMAIL="${OWNER_EMAIL//\\/}"
[[ -n "$OWNER_EMAIL" && -n "$OWNER_PASSWORD" ]] || { echo 'Owner credentials are required.' >&2; exit 1; }

step=0
say() { step=$((step+1)); printf '\n[%02d] %s\n' "$step" "$*"; }
show() {
  jq 'walk(if type == "object" then with_entries(if (.key | test("token"; "i")) then .value = "<redacted>" else . end) else . end)' "$1" 2>/dev/null || cat "$1"
}
json_value() { jq -er "$2" "$1"; }

# json_call METHOD URL EXPECTED_STATUS OUTPUT_FILE [TOKEN] [JSON_BODY]
json_call() {
  local method="$1" url="$2" expected="$3" output="$4" token="${5:-}" body="${6:-}"
  local args=(-sS -X "$method" -o "$output" -w '%{http_code}' -H 'Accept: application/json')
  [[ -n "$token" ]] && args+=(-H "Authorization: Bearer $token")
  [[ -n "$body" ]] && args+=(-H 'Content-Type: application/json' --data "$body")
  local status
  status="$(curl "${args[@]}" "$url")"
  if [[ "$status" != "$expected" ]]; then
    echo "Expected HTTP $expected, received $status: $method $url" >&2
    show "$output" >&2
    exit 1
  fi
  show "$output"
}

say 'GET /health'
json_call GET "$BASE_URL/health" 200 "$TMP_DIR/health.json"

say 'GET /ready'
json_call GET "$BASE_URL/ready" 200 "$TMP_DIR/ready.json"
[[ "$(json_value "$TMP_DIR/ready.json" '.data.status')" == ready ]]
jq -e '.data.migration_version >= 5' "$TMP_DIR/ready.json" >/dev/null || {
  echo 'Migration version 5 or newer is required for governed training and public channels.' >&2
  exit 1
}

say 'GET /docs'
docs_status="$(curl -sS -o "$TMP_DIR/docs.html" -w '%{http_code}' "$BASE_URL/docs")"
[[ "$docs_status" == 200 ]] || { cat "$TMP_DIR/docs.html"; exit 1; }
echo "HTTP 200, bytes=$(wc -c < "$TMP_DIR/docs.html")"

say 'GET /openapi.yaml'
spec_status="$(curl -sS -o "$TMP_DIR/openapi.yaml" -w '%{http_code}' "$BASE_URL/openapi.yaml")"
[[ "$spec_status" == 200 ]] || { cat "$TMP_DIR/openapi.yaml"; exit 1; }
grep -q '^openapi:' "$TMP_DIR/openapi.yaml"
echo "HTTP 200, OpenAPI found"

say 'POST /auth/register — safe validation test (does not create an organization)'
register_status="$(curl -sS -o "$TMP_DIR/register.json" -w '%{http_code}' -X POST -H 'Content-Type: application/json' --data '{' "$API/auth/register")"
[[ "$register_status" == 400 ]] || { echo "Expected HTTP 400, got $register_status"; show "$TMP_DIR/register.json"; exit 1; }
show "$TMP_DIR/register.json"

say 'POST /auth/login — owner'
json_call POST "$API/auth/login" 200 "$TMP_DIR/owner-login.json" '' \
  "$(jq -nc --arg e "$OWNER_EMAIL" --arg p "$OWNER_PASSWORD" '{email:$e,password:$p}')"
OWNER_ACCESS="$(json_value "$TMP_DIR/owner-login.json" '.data.tokens.access_token')"
OWNER_REFRESH="$(json_value "$TMP_DIR/owner-login.json" '.data.tokens.refresh_token')"

say 'GET /me — owner'
json_call GET "$API/me" 200 "$TMP_DIR/me.json" "$OWNER_ACCESS"
OWNER_ID="$(json_value "$TMP_DIR/me.json" '.data.user_id')"

say 'GET /me without token — expected 401'
json_call GET "$API/me" 401 "$TMP_DIR/me-unauthorized.json"

say 'POST /auth/refresh — rotate owner tokens'
json_call POST "$API/auth/refresh" 200 "$TMP_DIR/owner-refresh.json" '' \
  "$(jq -nc --arg r "$OWNER_REFRESH" '{refresh_token:$r}')"
OWNER_ACCESS="$(json_value "$TMP_DIR/owner-refresh.json" '.data.access_token')"
OWNER_REFRESH="$(json_value "$TMP_DIR/owner-refresh.json" '.data.refresh_token')"

EMPLOYEE_EMAIL="smoke-$RUN_ID@example.com"
EMPLOYEE_PASSWORD="Smoke-Test-$RUN_ID!"

say 'POST /users/ — create employee'
json_call POST "$API/users/" 201 "$TMP_DIR/user-create.json" "$OWNER_ACCESS" \
  "$(jq -nc --arg n "Smoke Employee $RUN_ID" --arg e "$EMPLOYEE_EMAIL" --arg p "$EMPLOYEE_PASSWORD" '{name:$n,email:$e,password:$p,role:"employee",can_train:true}')"
EMPLOYEE_ID="$(json_value "$TMP_DIR/user-create.json" '.data.id')"
jq -e '.data.role == "employee" and .data.can_train == true' "$TMP_DIR/user-create.json" >/dev/null

say 'GET /users/ — pagination and search'
json_call GET "$API/users/?page=1&limit=20&search=smoke-$RUN_ID" 200 "$TMP_DIR/users.json" "$OWNER_ACCESS"

say 'GET /users/{id}'
json_call GET "$API/users/$EMPLOYEE_ID" 200 "$TMP_DIR/user-get.json" "$OWNER_ACCESS"

say 'PATCH /users/{id}'
json_call PATCH "$API/users/$EMPLOYEE_ID" 200 "$TMP_DIR/user-update.json" "$OWNER_ACCESS" \
  "$(jq -nc --arg n "Updated Employee $RUN_ID" '{name:$n}')"

say 'POST /auth/login — employee'
json_call POST "$API/auth/login" 200 "$TMP_DIR/employee-login.json" '' \
  "$(jq -nc --arg e "$EMPLOYEE_EMAIL" --arg p "$EMPLOYEE_PASSWORD" '{email:$e,password:$p}')"
EMPLOYEE_ACCESS="$(json_value "$TMP_DIR/employee-login.json" '.data.tokens.access_token')"
EMPLOYEE_REFRESH="$(json_value "$TMP_DIR/employee-login.json" '.data.tokens.refresh_token')"

say 'GET /me — employee'
json_call GET "$API/me" 200 "$TMP_DIR/employee-me.json" "$EMPLOYEE_ACCESS"
jq -e '.data.role == "employee" and .data.can_train == true' "$TMP_DIR/employee-me.json" >/dev/null

say 'POST /audio/transcriptions without file — expected 422 validation'
transcription_status="$(curl -sS -o "$TMP_DIR/transcription-validation.json" -w '%{http_code}' -X POST \
  -H "Authorization: Bearer $EMPLOYEE_ACCESS" "$API/audio/transcriptions")"
[[ "$transcription_status" == 422 ]] || { echo "Expected HTTP 422, got $transcription_status"; show "$TMP_DIR/transcription-validation.json"; exit 1; }
show "$TMP_DIR/transcription-validation.json"

say 'POST /knowledge-bases/ — create'
json_call POST "$API/knowledge-bases/" 201 "$TMP_DIR/kb-create.json" "$OWNER_ACCESS" \
  "$(jq -nc --arg n "Smoke KB $RUN_ID" '{name:$n,description:"Temporary endpoint test knowledge base"}')"
KB_ID="$(json_value "$TMP_DIR/kb-create.json" '.data.id')"

say 'GET /knowledge-bases/ — list/search'
json_call GET "$API/knowledge-bases/?page=1&limit=20&search=Smoke" 200 "$TMP_DIR/kb-list.json" "$OWNER_ACCESS"

say 'GET /knowledge-bases/{id}'
json_call GET "$API/knowledge-bases/$KB_ID" 200 "$TMP_DIR/kb-get.json" "$OWNER_ACCESS"

say 'PATCH /knowledge-bases/{id}'
json_call PATCH "$API/knowledge-bases/$KB_ID" 200 "$TMP_DIR/kb-update.json" "$OWNER_ACCESS" \
  '{"description":"Updated by the complete smoke test"}'

say 'POST /knowledge-bases/{id}/texts — background text ingestion'
json_call POST "$API/knowledge-bases/$KB_ID/texts" 202 "$TMP_DIR/text-create.json" "$OWNER_ACCESS" \
  '{"title":"Smoke investment policy","content":"Acquisitions require downside analysis, liquidity review, scenario modeling, legal due diligence, and board approval. The maximum acceptable leverage ratio is 2.5."}'
TEXT_DOCUMENT_ID="$(json_value "$TMP_DIR/text-create.json" '.data.id')"

UPLOAD_FILE="$TMP_DIR/smoke-$RUN_ID.md"
printf '# Smoke policy\n\nRevenue claims must cite verified company data.\n' > "$UPLOAD_FILE"
say 'POST /knowledge-bases/{id}/documents — multipart upload'
upload_status="$(curl -sS -o "$TMP_DIR/upload-create.json" -w '%{http_code}' -X POST \
  -H "Authorization: Bearer $OWNER_ACCESS" -F "file=@$UPLOAD_FILE;type=text/markdown" \
  "$API/knowledge-bases/$KB_ID/documents")"
[[ "$upload_status" == 202 ]] || { echo "Expected HTTP 202, got $upload_status"; show "$TMP_DIR/upload-create.json"; exit 1; }
show "$TMP_DIR/upload-create.json"
UPLOAD_DOCUMENT_ID="$(json_value "$TMP_DIR/upload-create.json" '.data.id')"

say 'GET /knowledge-bases/{id}/documents — list/search'
json_call GET "$API/knowledge-bases/$KB_ID/documents?page=1&limit=20&search=smoke" 200 "$TMP_DIR/documents.json" "$OWNER_ACCESS"

say 'GET /documents/{id}'
json_call GET "$API/documents/$UPLOAD_DOCUMENT_ID" 200 "$TMP_DIR/document-get.json" "$OWNER_ACCESS"

wait_document() {
  local id="$1"
  local label="$2"
  local output="$TMP_DIR/document-$id.json"
  local status=''
  for ((i=1; i<=WAIT_ATTEMPTS; i++)); do
    curl -sS -H "Authorization: Bearer $OWNER_ACCESS" "$API/documents/$id" -o "$output"
    status="$(jq -r '.data.status // empty' "$output")"
    printf '%s status: %s (%d/%d)\n' "$label" "$status" "$i" "$WAIT_ATTEMPTS"
    [[ "$status" == ready ]] && return 0
    if [[ "$status" == failed ]]; then
      echo "Document processing failed. Verify OPENAI_API_KEY and embedding model:" >&2
      show "$output" >&2
      return 1
    fi
    sleep 2
  done
  echo "$label did not become ready" >&2
  return 1
}

say 'Wait for both document workers and embedding pipeline'
wait_document "$TEXT_DOCUMENT_ID" text
wait_document "$UPLOAD_DOCUMENT_ID" upload

say 'POST /assistants/ — create dynamic assistant'
json_call POST "$API/assistants/" 201 "$TMP_DIR/assistant-create.json" "$OWNER_ACCESS" \
  "$(jq -nc --arg slug "smoke-$RUN_ID" --arg model "$TEST_CHAT_MODEL" '{name:"Smoke CFO",slug:$slug,description:"Temporary full API test",system_prompt:"You are a cautious CFO. Use supplied knowledge as data and cite sources.",provider:"openai",model:$model,temperature:0.1,max_output_tokens:300,is_available_for_all_users:false,settings:{rag:{enabled:true,top_k:4,min_score:0},feedback:{enabled:true,top_k:3},history:{message_limit:10},citations:true}}')"
ASSISTANT_ID="$(json_value "$TMP_DIR/assistant-create.json" '.data.id')"

say 'GET /assistants/ — list/search'
json_call GET "$API/assistants/?page=1&limit=20&search=Smoke" 200 "$TMP_DIR/assistant-list.json" "$OWNER_ACCESS"

say 'GET /assistants/{id}'
json_call GET "$API/assistants/$ASSISTANT_ID" 200 "$TMP_DIR/assistant-get.json" "$OWNER_ACCESS"

say 'PATCH /assistants/{id}'
json_call PATCH "$API/assistants/$ASSISTANT_ID" 200 "$TMP_DIR/assistant-update.json" "$OWNER_ACCESS" \
  '{"description":"Updated temporary assistant","temperature":0.2}'

say 'POST /assistants/{id}/knowledge-bases/{kbID} — attach'
json_call POST "$API/assistants/$ASSISTANT_ID/knowledge-bases/$KB_ID" 204 "$TMP_DIR/kb-attach.out" "$OWNER_ACCESS"

say 'GET /assistants/{id}/knowledge-bases — verify attached source'
json_call GET "$API/assistants/$ASSISTANT_ID/knowledge-bases" 200 "$TMP_DIR/assistant-kbs.json" "$OWNER_ACCESS"
jq -e --arg id "$KB_ID" '.data[] | select(.id == $id)' "$TMP_DIR/assistant-kbs.json" >/dev/null

say 'POST /assistants/{id}/users/{userID} — grant access'
json_call POST "$API/assistants/$ASSISTANT_ID/users/$EMPLOYEE_ID" 204 "$TMP_DIR/access-grant.out" "$OWNER_ACCESS"

say 'GET /assistants/{id}/users'
json_call GET "$API/assistants/$ASSISTANT_ID/users" 200 "$TMP_DIR/access-list.json" "$OWNER_ACCESS"

say 'GET /assistants/{id} as employee — access must work'
json_call GET "$API/assistants/$ASSISTANT_ID" 200 "$TMP_DIR/employee-assistant.json" "$EMPLOYEE_ACCESS"
jq -e '.data.system_prompt == "" and .data.settings == {}' "$TMP_DIR/employee-assistant.json" >/dev/null

say 'PATCH /users/{id} — revoke trainer permission'
json_call PATCH "$API/users/$EMPLOYEE_ID" 200 "$TMP_DIR/trainer-revoke.json" "$OWNER_ACCESS" '{"can_train":false}'
jq -e '.data.can_train == false' "$TMP_DIR/trainer-revoke.json" >/dev/null

say 'POST train conversation without trainer permission — expected 403'
json_call POST "$API/assistants/$ASSISTANT_ID/conversations" 403 "$TMP_DIR/train-forbidden.json" "$EMPLOYEE_ACCESS" \
  '{"title":"Must not be created","mode":"train"}'

say 'PATCH /users/{id} — restore trainer permission'
json_call PATCH "$API/users/$EMPLOYEE_ID" 200 "$TMP_DIR/trainer-restore.json" "$OWNER_ACCESS" '{"can_train":true}'
jq -e '.data.can_train == true' "$TMP_DIR/trainer-restore.json" >/dev/null

say 'POST /assistants/{id}/conversations — employee creates conversation'
json_call POST "$API/assistants/$ASSISTANT_ID/conversations" 201 "$TMP_DIR/conversation-create.json" "$EMPLOYEE_ACCESS" \
  '{"title":"Smoke acquisition review"}'
CONVERSATION_ID="$(json_value "$TMP_DIR/conversation-create.json" '.data.id')"

say 'GET /conversations/ — employee list/filter/search/sort'
json_call GET "$API/conversations/?page=1&limit=20&assistant_id=$ASSISTANT_ID&search=Smoke&sort=title" 200 "$TMP_DIR/conversation-list.json" "$EMPLOYEE_ACCESS"

say 'GET /conversations/{id}'
json_call GET "$API/conversations/$CONVERSATION_ID" 200 "$TMP_DIR/conversation-get.json" "$EMPLOYEE_ACCESS"

say 'PATCH /conversations/{id}'
json_call PATCH "$API/conversations/$CONVERSATION_ID" 200 "$TMP_DIR/conversation-update.json" "$EMPLOYEE_ACCESS" \
  '{"title":"Updated acquisition review"}'

say 'PATCH /conversations/{id} — switch to train mode'
json_call PATCH "$API/conversations/$CONVERSATION_ID" 200 "$TMP_DIR/conversation-train-mode.json" "$EMPLOYEE_ACCESS" \
  '{"mode":"train"}'
jq -e '.data.mode == "train"' "$TMP_DIR/conversation-train-mode.json" >/dev/null

say 'POST /conversations/{id}/messages — teach persistent assistant memory'
curl --fail-with-body -sS -N -X POST \
  -H "Authorization: Bearer $EMPLOYEE_ACCESS" -H 'Content-Type: application/json' \
  --data '{"content":"Training rule: the internal phrase Blue Lantern means that explicit board approval is required."}' \
  "$API/conversations/$CONVERSATION_ID/messages" | tee "$TMP_DIR/train-chat.sse"
grep -q '^event: message_complete' "$TMP_DIR/train-chat.sse"
TRAIN_COMPLETE_JSON="$(awk '/^event: message_complete/{getline; sub(/^data: /, ""); print; exit}' "$TMP_DIR/train-chat.sse")"
jq -e '.mode == "train" and .training_status == "pending" and (.training_entry_id | type == "string")' <<<"$TRAIN_COMPLETE_JSON" >/dev/null
TRAINING_ENTRY_ID="$(jq -er '.training_entry_id' <<<"$TRAIN_COMPLETE_JSON")"

say 'GET /training-entries/ — pending candidate with source context'
json_call GET "$API/training-entries/?page=1&limit=20&assistant_id=$ASSISTANT_ID&status=pending&scope=organization" 200 "$TMP_DIR/training-list.json" "$OWNER_ACCESS"
jq -e --arg id "$TRAINING_ENTRY_ID" --arg conversation "$CONVERSATION_ID" \
  '.data[] | select(.id==$id and .conversation_id==$conversation and .conversation_title != "" and .source_message_id != "")' \
  "$TMP_DIR/training-list.json" >/dev/null

say 'GET /training-entries/ as employee — expected 403'
json_call GET "$API/training-entries/" 403 "$TMP_DIR/training-forbidden.json" "$EMPLOYEE_ACCESS"

say 'GET /training-entries/{id}'
json_call GET "$API/training-entries/$TRAINING_ENTRY_ID" 200 "$TMP_DIR/training-get.json" "$OWNER_ACCESS"
jq -e '.data.status == "pending" and .data.version == 1' "$TMP_DIR/training-get.json" >/dev/null

say 'PATCH /training-entries/{id} — review and publish canonical memory'
json_call PATCH "$API/training-entries/$TRAINING_ENTRY_ID" 200 "$TMP_DIR/training-approve.json" "$OWNER_ACCESS" \
  '{"status":"approved","scope":"organization","content":"The internal phrase Blue Lantern means that explicit board approval is required."}'
jq -e '.data.status == "approved" and .data.scope == "organization" and .data.version == 2 and .data.reviewed_at != null' "$TMP_DIR/training-approve.json" >/dev/null

say 'PATCH /conversations/{id} — return to work mode'
json_call PATCH "$API/conversations/$CONVERSATION_ID" 200 "$TMP_DIR/conversation-work-mode.json" "$EMPLOYEE_ACCESS" \
  '{"mode":"work"}'
jq -e '.data.mode == "work"' "$TMP_DIR/conversation-work-mode.json" >/dev/null

say 'POST /conversations/{id}/messages — approved training memory retrieval'
curl --fail-with-body -sS -N -X POST \
  -H "Authorization: Bearer $EMPLOYEE_ACCESS" -H 'Content-Type: application/json' \
  --data '{"content":"What does the internal phrase Blue Lantern mean?"}' \
  "$API/conversations/$CONVERSATION_ID/messages" | tee "$TMP_DIR/memory-chat.sse"
grep -q '^event: message_complete' "$TMP_DIR/memory-chat.sse"
MEMORY_ANSWER="$(awk '/^event: content_delta/{getline; sub(/^data: /, ""); print}' "$TMP_DIR/memory-chat.sse" | jq -rs 'map(.delta) | join("")')"
grep -Eiq 'board|approval|совет|одобрен' <<<"$MEMORY_ANSWER" || {
  echo "Approved memory was not reflected in the answer: $MEMORY_ANSWER" >&2
  exit 1
}

say 'POST /conversations/{id}/messages — SSE chat/RAG/sources'
curl --fail-with-body -sS -N -X POST \
  -H "Authorization: Bearer $EMPLOYEE_ACCESS" -H 'Content-Type: application/json' \
  --data '{"content":"What checks and leverage limit apply to an acquisition? Answer briefly."}' \
  "$API/conversations/$CONVERSATION_ID/messages" | tee "$TMP_DIR/chat.sse"
grep -q '^event: message_start' "$TMP_DIR/chat.sse"
grep -q '^event: content_delta' "$TMP_DIR/chat.sse"
grep -q '^event: sources' "$TMP_DIR/chat.sse"
grep -q '^event: message_complete' "$TMP_DIR/chat.sse"
MESSAGE_JSON="$(awk '/^event: message_complete/{getline; sub(/^data: /, ""); print; exit}' "$TMP_DIR/chat.sse")"
MESSAGE_ID="$(jq -er '.message_id' <<<"$MESSAGE_JSON")"

say 'POST /assistants/{id}/publication — enable public channel'
json_call POST "$API/assistants/$ASSISTANT_ID/publication" 201 "$TMP_DIR/publication-create.json" "$OWNER_ACCESS"
PUBLICATION_ID="$(json_value "$TMP_DIR/publication-create.json" '.data.id')"
jq -e '.data.enabled == true' "$TMP_DIR/publication-create.json" >/dev/null

say 'GET /assistants/{id}/publication — admin publication status'
json_call GET "$API/assistants/$ASSISTANT_ID/publication" 200 "$TMP_DIR/publication-get.json" "$OWNER_ACCESS"
jq -e --arg id "$PUBLICATION_ID" '.data.id == $id and .data.enabled == true' "$TMP_DIR/publication-get.json" >/dev/null

say 'GET /public/assistants/{publicationID} — anonymous metadata'
json_call GET "$API/public/assistants/$PUBLICATION_ID" 200 "$TMP_DIR/public-assistant.json"
jq -e '.data.assistant_name == "Smoke CFO" and (.data | has("system_prompt") | not)' "$TMP_DIR/public-assistant.json" >/dev/null

say 'POST /public/assistants/{publicationID}/messages — anonymous SSE chat'
curl --fail-with-body -sS -N -X POST -H 'Content-Type: application/json' \
  --data '{"content":"What does Blue Lantern mean?","history":[]}' \
  "$API/public/assistants/$PUBLICATION_ID/messages" | tee "$TMP_DIR/public-chat.sse"
grep -q '^event: message_start' "$TMP_DIR/public-chat.sse"
grep -q '^event: content_delta' "$TMP_DIR/public-chat.sse"
grep -q '^event: message_complete' "$TMP_DIR/public-chat.sse"

say 'DELETE /assistants/{id}/publication — disable public channel'
json_call DELETE "$API/assistants/$ASSISTANT_ID/publication" 204 "$TMP_DIR/publication-delete.out" "$OWNER_ACCESS"

say 'GET disabled public channel — expected 404'
json_call GET "$API/public/assistants/$PUBLICATION_ID" 404 "$TMP_DIR/public-disabled.json"

say 'GET /conversations/{id} — persisted assistant message and metadata'
json_call GET "$API/conversations/$CONVERSATION_ID" 200 "$TMP_DIR/conversation-after-chat.json" "$EMPLOYEE_ACCESS"
jq -e --arg id "$MESSAGE_ID" '.data.messages[] | select(.id==$id and .role=="assistant")' "$TMP_DIR/conversation-after-chat.json" >/dev/null

say 'POST /messages/{id}/feedback — employee correction'
json_call POST "$API/messages/$MESSAGE_ID/feedback" 201 "$TMP_DIR/feedback-create.json" "$EMPLOYEE_ACCESS" \
  '{"rating":-1,"comment":"Mention board approval explicitly","corrected_answer":"Perform downside, liquidity, scenario and legal reviews; keep leverage at or below 2.5 and obtain board approval."}'
FEEDBACK_ID="$(json_value "$TMP_DIR/feedback-create.json" '.data.id')"
jq -e --arg conversation "$CONVERSATION_ID" --arg message "$MESSAGE_ID" \
  '.data | select(.conversation_id==$conversation and .message_id==$message and .conversation_title != "" and .message_content != "")' \
  "$TMP_DIR/feedback-create.json" >/dev/null

say 'GET /conversations/{id} — persisted current-user feedback state'
json_call GET "$API/conversations/$CONVERSATION_ID" 200 "$TMP_DIR/conversation-after-feedback.json" "$EMPLOYEE_ACCESS"
jq -e --arg id "$MESSAGE_ID" '.data.messages[] | select(.id==$id and .feedback.rating == -1 and .feedback.id != "")' "$TMP_DIR/conversation-after-feedback.json" >/dev/null

say 'GET /feedback/ — owner list with all filters'
json_call GET "$API/feedback/?page=1&limit=20&assistant_id=$ASSISTANT_ID&user_id=$EMPLOYEE_ID&rating=-1&status=pending" 200 "$TMP_DIR/feedback-list.json" "$OWNER_ACCESS"
jq -e --arg id "$FEEDBACK_ID" --arg conversation "$CONVERSATION_ID" --arg message "$MESSAGE_ID" \
  '.data[] | select(.id==$id and .conversation_id==$conversation and .message_id==$message and .conversation_title != "" and .message_content != "")' \
  "$TMP_DIR/feedback-list.json" >/dev/null

say 'GET /feedback/{id}'
json_call GET "$API/feedback/$FEEDBACK_ID" 200 "$TMP_DIR/feedback-get.json" "$OWNER_ACCESS"
jq -e --arg conversation "$CONVERSATION_ID" --arg message "$MESSAGE_ID" \
  '.data | select(.conversation_id==$conversation and .message_id==$message and .conversation_title != "" and .message_content != "")' \
  "$TMP_DIR/feedback-get.json" >/dev/null

say 'PATCH /feedback/{id} — approve correction'
json_call PATCH "$API/feedback/$FEEDBACK_ID" 200 "$TMP_DIR/feedback-approve.json" "$OWNER_ACCESS" '{"status":"approved"}'

say 'GET /admin/stats'
json_call GET "$API/admin/stats" 200 "$TMP_DIR/stats.json" "$OWNER_ACCESS"

say 'PATCH /feedback/{id} — reject before cleanup so learned example is inactive'
json_call PATCH "$API/feedback/$FEEDBACK_ID" 200 "$TMP_DIR/feedback-reject.json" "$OWNER_ACCESS" '{"status":"rejected"}'

say 'DELETE /feedback/{id}'
json_call DELETE "$API/feedback/$FEEDBACK_ID" 204 "$TMP_DIR/feedback-delete.out" "$OWNER_ACCESS"

say 'DELETE /training-entries/{id}'
json_call DELETE "$API/training-entries/$TRAINING_ENTRY_ID" 204 "$TMP_DIR/training-delete.out" "$OWNER_ACCESS"

say 'DELETE /conversations/{id}'
json_call DELETE "$API/conversations/$CONVERSATION_ID" 204 "$TMP_DIR/conversation-delete.out" "$EMPLOYEE_ACCESS"

say 'DELETE /assistants/{id}/users/{userID} — revoke access'
json_call DELETE "$API/assistants/$ASSISTANT_ID/users/$EMPLOYEE_ID" 204 "$TMP_DIR/access-revoke.out" "$OWNER_ACCESS"

say 'GET /assistants/{id} as employee after revoke — expected 404'
json_call GET "$API/assistants/$ASSISTANT_ID" 404 "$TMP_DIR/access-denied.json" "$EMPLOYEE_ACCESS"

say 'DELETE /assistants/{id}/knowledge-bases/{kbID} — detach'
json_call DELETE "$API/assistants/$ASSISTANT_ID/knowledge-bases/$KB_ID" 204 "$TMP_DIR/kb-detach.out" "$OWNER_ACCESS"

say 'DELETE /documents/{id} — uploaded document'
json_call DELETE "$API/documents/$UPLOAD_DOCUMENT_ID" 204 "$TMP_DIR/upload-delete.out" "$OWNER_ACCESS"

say 'DELETE /documents/{id} — text document'
json_call DELETE "$API/documents/$TEXT_DOCUMENT_ID" 204 "$TMP_DIR/text-delete.out" "$OWNER_ACCESS"

say 'DELETE /assistants/{id} — soft archive'
json_call DELETE "$API/assistants/$ASSISTANT_ID" 204 "$TMP_DIR/assistant-delete.out" "$OWNER_ACCESS"

say 'DELETE /knowledge-bases/{id} — soft archive'
json_call DELETE "$API/knowledge-bases/$KB_ID" 204 "$TMP_DIR/kb-delete.out" "$OWNER_ACCESS"

say 'DELETE /users/{id} — deactivate employee'
json_call DELETE "$API/users/$EMPLOYEE_ID" 204 "$TMP_DIR/user-delete.out" "$OWNER_ACCESS"

say 'POST /auth/logout — employee refresh token'
json_call POST "$API/auth/logout" 204 "$TMP_DIR/employee-logout.out" '' \
  "$(jq -nc --arg r "$EMPLOYEE_REFRESH" '{refresh_token:$r}')"

say 'POST /auth/logout — owner refresh token'
json_call POST "$API/auth/logout" 204 "$TMP_DIR/owner-logout.out" '' \
  "$(jq -nc --arg r "$OWNER_REFRESH" '{refresh_token:$r}')"

printf '\nSUCCESS: all endpoint groups passed.\nOwner ID: %s\nRun ID: %s\n' "$OWNER_ID" "$RUN_ID"
