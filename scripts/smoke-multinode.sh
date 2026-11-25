#!/usr/bin/env bash
set -euo pipefail

control_image=${1:?Pass the Moori image}
agent_image=${2:?Pass the Agent image}
fixture_image=${MOORI_SMOKE_IMAGE:-alpine:3.22}
work=$(mktemp -d)
suffix=${work##*.}
project=moorismoke${suffix,,}
local_target=$project-local
remote_target=$project-remote
volume=$project-data
control=$project-control
agent=$project-agent
engine=$project-engine
network=$project-net
socket=$work/remote/docker.sock
mkdir "$work/control" "$work/remote"

cleanup() {
  local status=$? id compose_project
  trap - EXIT
  if ((status)); then
    for id in "$control" "$agent" "$engine"; do docker logs --tail 60 "$id" 2>/dev/null || true; done
  fi
  docker ps -aq --filter "label=moori.smoke=$project" | while read -r id; do
    compose_project=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$id") || continue
    docker rm -fv "$id" >/dev/null || true
    [[ -z $compose_project ]] || docker network ls -q --filter "label=com.docker.compose.project=$compose_project" |
      while read -r net; do docker network rm "$net" >/dev/null 2>&1 || true; done
  done || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
  docker rm -fv "$agent" "$engine" "$control" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  rm -rf -- "$work" || true
  exit "$status"
}
trap cleanup EXIT

remote() { docker exec "$engine" docker --host unix:///smoke/docker.sock "$@"; }
admin() { docker exec "$control" moori "$@"; }
wait_ready() {
  local start=$SECONDS
  until "$@" >/dev/null 2>&1; do
    if ((SECONDS - start >= 90)); then echo "Timed out: $1" >&2; return 1; fi
    sleep 1
  done
}

docker network create "$network" >/dev/null
docker run -d --privileged --name "$engine" --network "$network" \
  -v "$work/remote:/smoke" --entrypoint dockerd docker:28-dind \
  --host=unix:///smoke/docker.sock --storage-driver=vfs >/dev/null
wait_ready remote info
docker run -d --name "$control" --network "$network" -p 127.0.0.1::8080 \
  -v /var/run/docker.sock:/var/run/docker.sock -v "$work/control:/smoke" \
  "$control_image" serve -local-execution on >/dev/null
base=http://$(docker port "$control" 8080/tcp)
wait_ready curl -fsS --max-time 3 "$base/healthz"
MOORI_AGENT_TOKEN=$(admin issue-agent-credential -agent smoke-agent)
export MOORI_AGENT_TOKEN
docker run -d --name "$agent" --network "$network" -e MOORI_AGENT_TOKEN \
  -e "MOORI_ENDPOINT=ws://$control:8080/agent/v1/connect" \
  -v "$socket:/var/run/docker.sock" "$agent_image" >/dev/null
unset MOORI_AGENT_TOKEN

admin register-target -project "$project" -target "$local_target" -agent @local
admin register-target -project "$project" -target "$remote_target" -agent smoke-agent
local_token=$(admin issue-ci-token -id smoke-local -project "$project" -target "$local_target" -ttl 20m)
remote_token=$(admin issue-ci-token -id smoke-remote -project "$project" -target "$remote_target" -ttl 20m)
docker pull "$fixture_image" >/dev/null
ref=$(docker image inspect --format '{{index .RepoDigests 0}}' "$fixture_image")
expected_image=$(docker image inspect --format '{{.Id}}' "$ref")
cat >"$work/control/compose.yml" <<EOF
services:
  app:
    image: $ref
    command: ["sleep", "3600"]
    volumes: ["data:/data"]
    labels: {moori.smoke: "$project"}
volumes:
  data: {name: "$volume", external: true}
EOF
admin publish-release -project "$project" -release smoke -compose /smoke/compose.yml

submit() {
  local target=$1 token=$2 key=$3
  curl -fsS --max-time 10 -H "Authorization: Bearer $token" -H "Idempotency-Key: $key" \
    -H 'Content-Type: application/json' \
    --data "$(jq -nc --arg project "$project" --arg target "$target" '{project:$project,target:$target,release:"smoke"}')" \
    "$base/api/v1/deployments" | jq -er .id
}

expect_state() {
  local id=$1 token=$2 expected=$3 response state start=$SECONDS
  while ((SECONDS - start < 180)); do
    response=$(curl -fsS --max-time 10 -H "Authorization: Bearer $token" "$base/api/v1/deployments/$id")
    state=$(jq -er .state <<<"$response")
    case "$state" in
      "$expected") echo "$id $state"; return 0 ;;
      succeeded|failed|canceled|abandoned|reconciling|needs_attention) echo "$response" >&2; return 1 ;;
    esac
    sleep 2
  done
  echo "Timed out waiting for $id to reach $expected (last: ${state:-unknown})" >&2
  return 1
}

missing_external_volume_logged() {
  curl -fsS --max-time 5 -H "Authorization: Bearer $remote_token" "$base/api/v1/deployments/$remote_id/logs" |
    jq -e --arg volume "$volume" '.text | contains("external Docker volumes must exist on the execution node (" + "[" + $volume + "]" + ")")' >/dev/null
}

# The remote preflight fails before running Compose; local has the required volume.
docker volume create "$volume" >/dev/null
local_id=$(submit "$local_target" "$local_token" local-first)
remote_id=$(submit "$remote_target" "$remote_token" remote-missing-volume)
expect_state "$local_id" "$local_token" succeeded
expect_state "$remote_id" "$remote_token" failed
wait_ready missing_external_volume_logged
remote volume create "$volume" >/dev/null

retry_id=$(submit "$remote_target" "$remote_token" remote-retry)
[[ $retry_id != "$remote_id" && $retry_id != "$local_id" ]]
expect_state "$retry_id" "$remote_token" succeeded
expect_state "$local_id" "$local_token" succeeded
local_container=$(docker ps -q --filter "label=moori.smoke=$project")
remote_container=$(remote ps -q --filter "label=moori.smoke=$project")
[[ $(docker inspect --format '{{.Image}}' "$local_container") == "$expected_image" ]]
[[ $(remote inspect --format '{{.Image}}' "$remote_container") == "$expected_image" ]]
echo "Both engines are running the same image: $ref"
