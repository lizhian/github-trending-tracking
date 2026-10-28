#!/usr/bin/env bash
set -euo pipefail

image="${1:?Usage: scripts/verify-image.sh IMAGE PLATFORM}"
platform="${2:?Specify linux/amd64 or linux/arm64}"
case "$platform" in linux/amd64|linux/arm64) ;; *) exit 2 ;; esac

work="$(mktemp -d)"
container="gtt-image-check-$$-$RANDOM"
cleanup() {
  result=$?
  if [ "$result" -ne 0 ]; then docker logs "$container" 2>/dev/null || true; fi
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
mkdir "$work/data" "$work/backups"
chmod 0777 "$work/data" "$work/backups"

docker run --rm --platform "$platform" \
  --mount "type=bind,src=$work/data,dst=/data" "$image" init
docker run -d --name "$container" --platform "$platform" \
  -p 127.0.0.1::8080 \
  --mount "type=bind,src=$work/data,dst=/data" \
  --mount "type=bind,src=$work/backups,dst=/backups" \
  -e BACKUP_DIR=/backups -e ALLOWED_ORIGIN=http://localhost \
  "$image" >/dev/null
address="$(docker port "$container" 8080/tcp)"
ready=false
for attempt in {1..30}; do
  if curl --silent --fail --max-time 2 "http://$address/api/projects" > "$work/list.json"; then
    ready=true
    break
  fi
  sleep 1
done
test "$ready" = true
jq -e '.items == [] and .total == 0 and .pages == 1 and (.summary | type == "object") and (.status | type == "object")' "$work/list.json" >/dev/null
curl --silent --fail --max-time 5 "http://$address/" > "$work/index.html"
grep -Fq '<title>' "$work/index.html"
asset="$(sed -n 's/.*src="\(\/assets\/[^" ]*\)".*/\1/p' "$work/index.html" | head -n 1)"
test -n "$asset"
curl --silent --fail --max-time 5 "http://$address$asset" -o /dev/null
echo "Verified $platform: static frontend, bundled asset and SQLite API."
