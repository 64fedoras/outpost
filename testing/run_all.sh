#!/bin/sh
# One entry point for every automatic check. Run it from anywhere.
#
#   sh testing/run_all.sh          same as "fast"
#   sh testing/run_all.sh fast     go vet, unit tests, JavaScript syntax check, cross-build of all five platforms
#   sh testing/run_all.sh race     unit tests with the race detector (needs gcc for cgo)
#   sh testing/run_all.sh e2e      starts the local rig, runs the browser scripts, stops the rig
#   sh testing/run_all.sh all      fast, then race, then e2e
#
# CI runs "fast" and "race" (.github/workflows/ci.yml). "e2e" needs Python Playwright
# and Chromium (see testing/README.md); it stops with an error if they are missing
# rather than skipping, so a green run always means the scripts ran.
# E2E_FULL=1 adds the slow scripts (flow.py, about 4 minutes, and soak.py).
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

step() { printf '\n== %s\n' "$*"; }

fast() {
	step "go vet"
	go vet ./...
	step "go test"
	go test ./...
	step "JavaScript syntax (node --check)"
	for f in web/*.js; do
		echo "check $f"
		node --check "$f"
	done
	step "cross-build all platforms"
	for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
		echo "build $t"
		CGO_ENABLED=0 GOOS=${t%/*} GOARCH=${t#*/} go build -o /dev/null ./cmd/outpost
	done
}

race() {
	step "go test -race"
	CGO_ENABLED=1 go test -race ./...
}

e2e() {
	if ! python3 -c 'import playwright' 2>/dev/null; then
		echo "e2e needs Python Playwright: pip install playwright && playwright install chromium" >&2
		exit 1
	fi
	export OUTPOST_E2E_DIR="$ROOT/testing/e2e/run"
	trap 'sh "$ROOT/testing/e2e/run.sh" down >/dev/null 2>&1 || true' EXIT
	step "start the rig"
	sh testing/e2e/run.sh up
	scripts="steer.py edit.py rerun.py taskmenu.py mobile.py dropzone.py projectnew.py"
	if [ "${E2E_FULL:-}" = 1 ]; then scripts="$scripts flow.py soak.py"; fi
	for s in $scripts; do
		step "e2e: $s"
		python3 "testing/e2e/$s"
	done
}

case "${1:-fast}" in
fast) fast ;;
race) race ;;
e2e) e2e ;;
all) fast; race; e2e ;;
*) echo "usage: sh testing/run_all.sh [fast|race|e2e|all]" >&2; exit 2 ;;
esac
step "done: ${1:-fast} passed"
