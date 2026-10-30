#!/bin/sh
# Records demo/demo.gif from a made-up inbox. It runs gh inbox against
# fakeapi, which serves the host github.localhost through HTTP_PROXY, with a
# dummy token and its own cache and config. It needs Python 3 with Pillow
# and the DejaVu fonts.
set -eu

cd "$(dirname "$0")/.."
work=$(mktemp -d)
trap 'kill "$api" 2>/dev/null; rm -rf "$work"' EXIT

go build -o "$work/gh-inbox" .
go build -o "$work/fakeapi" ./demo/fakeapi
"$work/fakeapi" -addr 127.0.0.1:8765 2>"$work/fakeapi.log" &
api=$!
sleep 1

export HTTP_PROXY=http://127.0.0.1:8765 http_proxy=http://127.0.0.1:8765 NO_PROXY= no_proxy=
export GH_HOST=github.localhost GH_TOKEN=demo GH_ENTERPRISE_TOKEN=demo
export XDG_CACHE_HOME="$work/cache" XDG_STATE_HOME="$work/state" XDG_CONFIG_HOME="$work/config" GH_CONFIG_DIR="$work/gh"
# "o" in triage opens nothing.
export BROWSER=true

python3 demo/capture.py "$work/gh-inbox" "$work/scenes.json"
python3 demo/render.py "$work/scenes.json" demo/demo.gif
echo "Wrote demo/demo.gif"
