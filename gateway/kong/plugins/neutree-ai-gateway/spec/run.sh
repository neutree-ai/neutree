#!/bin/sh
# Run the neutree-ai-gateway Lua unit tests.
#
# The tests must run under LuaJIT (the OpenResty/Kong runtime), not PUC Lua 5.1:
# handler.lua uses `goto`/labels and `string.buffer`, which PUC 5.1 cannot even
# parse. The luarocks `busted` wrapper hard-execs `lua5.1`, so we bypass it and
# invoke busted's Lua entry point directly under `luajit`.
#
# Requires on PATH: luajit, luarocks, and the rocks `lua-cjson` + `busted`
# installed. `make gateway-lua-test` builds spec/Dockerfile (which bakes those
# into cached layers) and runs this script inside it; CI runs the same target.
set -e

plugin_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$plugin_dir"

# Load all producers, the neutral contract and the consumer in one namespace.
eval "$(luarocks path)"
gateway_dir=$(CDPATH= cd -- "$plugin_dir/../../.." && pwd)
export LUA_PATH="$gateway_dir/?.lua;$LUA_PATH"

busted_bin=$(ls /usr/local/lib/luarocks/rocks-*/busted/*/bin/busted 2>/dev/null | head -1)
if [ -z "$busted_bin" ]; then
    busted_bin=$(ls "$HOME"/.luarocks/lib/luarocks/rocks-*/busted/*/bin/busted 2>/dev/null | head -1)
fi
if [ -z "$busted_bin" ]; then
    echo "busted not found — install it with: luarocks install busted" >&2
    exit 1
fi

exec luajit "$busted_bin" spec/ ../neutree-metrics/spec/ "$@"
