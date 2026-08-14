# Test image for the neutree-ai-gateway Lua unit tests.
#
# Bakes the toolchain (LuaJIT + luarocks + lua-cjson + busted) into cached
# layers so `make gateway-lua-test` only pays the install cost on first build;
# later runs are just `docker run`. LuaJIT is required because the plugin uses
# goto/labels + string.buffer, which PUC Lua 5.1 cannot parse. The luarocks
# `lua-cjson` is the OpenResty fork (array_mt / decode_array_with_array_mt),
# matching the gateway's runtime cjson behaviour.
#
# The plugin source is NOT baked in — it is bind-mounted at run time so tests
# always exercise the working tree. See Makefile: gateway-lua-test.
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive
RUN set -eux; \
    for attempt in 1 2 3 4 5; do \
        if apt-get update -o Acquire::Retries=10; then break; fi; \
        if [ "$attempt" -eq 5 ]; then exit 1; fi; \
        sleep 10; \
    done; \
    apt-get install -y --no-install-recommends -o Acquire::Retries=10 \
        luajit luarocks lua5.1 liblua5.1-0-dev build-essential unzip ca-certificates; \
    install_rock() { \
        local rock="$1"; \
        local attempt; \
        for attempt in 1 2 3 4 5; do \
            if luarocks install "$rock"; then return 0; fi; \
            sleep 10; \
        done; \
        return 1; \
    }; \
    install_rock lua-cjson; \
    install_rock busted; \
    rm -rf /var/lib/apt/lists/*

WORKDIR /plugin
