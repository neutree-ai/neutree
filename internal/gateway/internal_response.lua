local prefix = "x-neutree-internal-"
for name in pairs(ngx.resp.get_headers(0, true)) do
    if name:lower():gsub("_", "-"):sub(1, #prefix) == prefix then
        ngx.header[name] = nil
    end
end
local shared = kong.ctx.shared
if shared.neutree_internal_unpacked then
    local consumer = kong.client.get_consumer()
    for key, value in pairs({
        ["Consumer-Id"] = consumer and consumer.custom_id,
        ["Selected-Upstream"] = shared.neutree_selected_upstream,
        ["Upstream-Model"] = shared.neutree_upstream_model,
    }) do
        if type(value) == "string" and value ~= "" then
            kong.response.set_header(prefix .. key, value)
        end
    end
end
