-- The control plane prepends the expected token as a Lua string literal.
-- Use the full header table: an internal header past the PDK's default
-- 100-header window must be stripped too. Never select one of duplicate values.
local prefix = "x-neutree-internal-"
local fields = {}
for name, value in pairs(ngx.req.get_headers(0, true)) do
    local lower = name:lower():gsub("_", "-")
    if lower:sub(1, #prefix) == prefix then
        local key = lower:sub(#prefix + 1)
        fields[key] = fields[key] == nil and value or false
        ngx.req.clear_header(name)
    end
end
if expected ~= "" and fields.unpacked == "1" then
    if fields.token ~= expected then
        return kong.response.exit(401, { error = { message = "invalid internal gateway token", type = "authentication_error" } })
    end
    if type(fields.model) ~= "string" or not fields.model:match("%S") or
       (fields.stream ~= "0" and fields.stream ~= "1") then
        return kong.response.exit(400, { error = { message = "invalid internal gateway metadata", type = "invalid_request_error" } })
    end
    local shared = kong.ctx.shared
    shared.neutree_internal_unpacked = true
    shared.neutree_request_model = fields.model
    shared.neutree_request_stream = fields.stream == "1"
    for _, key in ipairs({ "prefix-hash", "block-hashes", "session" }) do
        if type(fields[key]) == "string" then
            kong.service.request.set_header(prefix .. key, fields[key])
        end
    end
end
