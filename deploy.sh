#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_PATH="${SCRIPT_DIR}/$(basename -- "${BASH_SOURCE[0]}")"

log() {
  printf '[deploy] %s\n' "$*"
}

fail() {
  printf '[deploy] ERROR: %s\n' "$*" >&2
  exit 1
}

on_error() {
  local exit_code=$?
  printf '[deploy] ERROR: command failed at line %s (exit %s)\n' "${BASH_LINENO[0]}" "${exit_code}" >&2
  exit "${exit_code}"
}

trap on_error ERR

for command_name in git docker curl; do
  command -v "${command_name}" >/dev/null 2>&1 || fail "${command_name} is required"
done

docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is required"

cd "${SCRIPT_DIR}"

if command -v flock >/dev/null 2>&1; then
  exec 9>/tmp/ai-assistan-backend-deploy.lock
  flock -n 9 || fail "another deployment is already running"
fi

[[ -f .env ]] || fail ".env is missing; copy .env.example to .env and configure production secrets"
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || fail "${SCRIPT_DIR} is not a Git repository"

current_branch="$(git symbolic-ref --quiet --short HEAD)" || fail "detached HEAD is not supported"
deploy_branch="${DEPLOY_BRANCH:-${current_branch}}"
deploy_remote="${DEPLOY_REMOTE:-origin}"

[[ "${current_branch}" == "${deploy_branch}" ]] || \
  fail "current branch is ${current_branch}; expected ${deploy_branch}"
git remote get-url "${deploy_remote}" >/dev/null 2>&1 || fail "Git remote ${deploy_remote} does not exist"

if ! git diff --quiet || ! git diff --cached --quiet; then
  fail "tracked files contain local changes; commit or discard them before deployment"
fi

if [[ "${AI_DEPLOY_SKIP_PULL:-0}" != "1" ]]; then
  commit_before="$(git rev-parse HEAD)"
  log "updating ${deploy_remote}/${deploy_branch} with fast-forward only"
  git pull --ff-only "${deploy_remote}" "${deploy_branch}"
  commit_after="$(git rev-parse HEAD)"

  if [[ "${commit_before}" != "${commit_after}" ]]; then
    log "code updated to ${commit_after:0:12}; restarting with the updated deploy script"
    exec env AI_DEPLOY_SKIP_PULL=1 "${SCRIPT_PATH}"
  fi

  log "repository is already current at ${commit_after:0:12}"
fi

log "validating Docker Compose configuration"
docker compose config --quiet

log "building the backend from the current commit"
docker compose build --pull backend

log "starting services"
if ! docker compose up -d --remove-orphans --wait; then
  docker compose ps
  docker compose logs --tail=100 backend >&2
  fail "Docker Compose could not start healthy services"
fi

app_port="$(sed -n 's/^APP_PORT=//p' .env | tail -n 1 | tr -d '\r\"[:space:]')"
app_port="${app_port:-18473}"
[[ "${app_port}" =~ ^[0-9]+$ ]] || fail "APP_PORT in .env must be numeric"

base_url="http://127.0.0.1:${app_port}"
log "checking ${base_url}/health and ${base_url}/ready"

health_ok=0
for _ in {1..30}; do
  if curl -fsS "${base_url}/health" >/dev/null && curl -fsS "${base_url}/ready" >/dev/null; then
    health_ok=1
    break
  fi
  sleep 2
done

if [[ "${health_ok}" != "1" ]]; then
  docker compose ps
  docker compose logs --tail=100 backend >&2
  fail "backend did not become ready"
fi

docker compose ps
ready_response="$(curl -fsS "${base_url}/ready")"
log "deployment completed: ${ready_response}"
