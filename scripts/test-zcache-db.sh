#!/usr/bin/env bash
# Run the complete database suite in its own disposable Compose project.
# Never reuse the standard test containers or live control-plane database.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
config=$(mktemp)
project="neutree-zcache-s1-test"
cleanup() {
  docker compose -p "$project" -f "$config" down -v --remove-orphans >/dev/null
  rm -f "$config"
}
trap cleanup EXIT
# Compose resolves mount paths before we relocate the generated configuration.
docker compose -f db/docker-compose.test.yml config --format json | python3 -c '
import json,sys
c=json.load(sys.stdin)
c["name"]="neutree-zcache-s1-test"
for key,s in c["services"].items():
 s["container_name"]="neutree-zcache-s1-test-"+key
 for p in s.get("ports",[]):
  p["published"]={5432:"15439",9999:"19999",6432:"16439"}[int(p["target"])]
  p["host_ip"]="127.0.0.1"
for n in c.get("networks",{}).values(): n["name"]="neutree-zcache-s1-test_default"
json.dump(c,sys.stdout)
' > "$config"
docker compose -p "$project" -f "$config" up --wait postgres auth
docker compose -p "$project" -f "$config" run --rm migration
docker compose -p "$project" -f "$config" run --rm seed
docker compose -p "$project" -f "$config" up -d postgrest
POSTGRES_PORT=15439 POSTGREST_URL=http://127.0.0.1:16439 GOTRUE_URL=http://127.0.0.1:19999 go test -count=1 -v ./db/dbtest
# Exercise both migration directions in this disposable DB only.
docker compose -p "$project" -f "$config" run --rm migration -source=file://migrations -database 'postgres://postgres:pgpassword@postgres:5432/neutree_test?sslmode=disable' down 1
docker compose -p "$project" -f "$config" run --rm migration
