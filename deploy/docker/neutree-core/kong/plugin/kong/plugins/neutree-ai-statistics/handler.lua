local cjson = require("cjson.safe")
local buffer = require("string.buffer")
local ai_shared = require("kong.llm.drivers.shared")
local ai_driver = require("kong.llm.drivers.openai")
local strip = require("kong.tools.string").strip

local AIStatisticsPluginHandler = {
    PRIORITY = 1000,
    VERSION = "0.0.1",
}

-- copy from https://github.com/Kong/kong/blob/c415a933fc71865372c0891e7aa5421b10e85901/kong/llm/plugin/shared-filters/normalize-sse-chunk.lua#L25
-- get the token text from an event frame
local function get_token_text(event_t)
    -- get: event_t.choices[1]
    local first_choice = ((event_t or EMPTY).choices or EMPTY)[1] or EMPTY
    -- return:
    --   - event_t.choices[1].delta.content
    --   - event_t.choices[1].text
    --   - ""
    local token_text = (first_choice.delta or EMPTY).content or first_choice.text or ""
    return (type(token_text) == "string" and token_text) or ""
end

local function handle_json_response(conf)
    local response_body = kong.service.response.get_raw_body()
    if response_body == nil then
        -- response body is nil, ignore
        return 
    end

    local ai_response, err = cjson.decode(response_body)
    if err then
        -- not json format, ignore
        return 
    end

    kong.ctx.plugin.response_model = ai_response.model
    if ai_response.usage then
        if conf.route_type == "/v1/chat/completions" then
            kong.ctx.plugin.completions_tokens = ai_response.usage.completion_tokens
            kong.ctx.plugin.prompt_tokens = ai_response.usage.prompt_tokens
            kong.ctx.plugin.total_tokens = ai_response.usage.total_tokens
        elseif conf.route_type == "/v1/embeddings" then
            kong.ctx.plugin.prompt_tokens = ai_response.usage.prompt_tokens
            kong.ctx.plugin.total_tokens = ai_response.usage.total_tokens
        elseif conf.route_type == "/v1/rerank" then
            kong.ctx.plugin.prompt_tokens = ai_response.usage.prompt_tokens
            kong.ctx.plugin.total_tokens = ai_response.usage.total_tokens
        end
    end
end

local function handle_stream_response(conf, chunk, finished)
    local content_type = kong.service.response.get_header("Content-Type")
    local normalized_content_type = content_type and content_type:sub(1, (content_type:find(";") or 0) - 1)
    if normalized_content_type and normalized_content_type ~= "text/event-stream" then
        return true
    end

    local body_buffer = kong.ctx.plugin.sse_body_buffer

    if type(chunk) == "string" and chunk ~= "" then
        local events = ai_shared.frame_to_events(chunk, normalized_content_type)
        if not events then
            return
        end
        
        local frame_buffer = buffer.new()
        for _, event in ipairs(events) do
            local formatted, _, metadata = ai_driver.from_format(event, "", "stream/llm/v1/chat")
            if formatted and formatted ~= ai_shared._CONST.SSE_TERMINATOR then 
                local event_t, err = cjson.decode(formatted)
                if not err then
                    local token_t = get_token_text(event_t)
                    body_buffer:put(token_t)
                    if kong.ctx.plugin.response_model == nil and event_t.model then
                        kong.ctx.plugin.response_model = event_t.model
                    end
                    -- some upstream stream api return token usage in the last event, if exist, record it.
                    if event_t.usage then
                        kong.ctx.plugin.prompt_tokens = event_t.usage.prompt_tokens
                        kong.ctx.plugin.completions_tokens = event_t.usage.completion_tokens
                        kong.ctx.plugin.total_tokens = event_t.usage.total_tokens
                    end
                end
            end
        end
    end

    if finished and kong.ctx.plugin.total_tokens == nil and body_buffer ~= nil then
        -- in this, means upstream stream api never return token usage, so we need to calucate it.
        local response = body_buffer:get()
        local completion_tokens_count = math.ceil(#strip(response) / 4)
        kong.ctx.plugin.completions_tokens = completion_tokens_count
        kong.ctx.plugin.total_tokens = kong.ctx.plugin.completions_tokens + kong.ctx.plugin.prompt_tokens
    end
end

local function fail(code, msg)
    return kong.response.exit(code, msg and { error = { message = msg } } or nil)
end

function AIStatisticsPluginHandler:access(conf)
    local request_path = kong.request.get_path()
    -- skip not match route_type
    if not string.match(request_path, conf.route_type.."$") then
        kong.ctx.plugin.skip = true
        return
    end
    local content_type = kong.request.get_header("Content-Type") or "application/json"
    if string.find(content_type, "application/json", nil, true) then
        local request_body = kong.request.get_raw_body()
        if request_body == nil then
            return fail(400, "request body can not be null")
        end
        local ai_request, err = cjson.decode(request_body)
        if err then
            return fail(400, "request body is not json format")
        end
        kong.ctx.plugin.request_model = ai_request.model
        if not ai_request.stream then
            kong.service.request.enable_buffering()
        else
            -- stream mode will may not return token usage, so we need to calucate prompt_tokens and record it.
            local prompt_tokens, err = ai_shared.calculate_cost(ai_request or {}, {}, 1.0)
            if err then
              kong.log.err("unable to estimate request token cost: ", err)
              return fail(500)
            end
            kong.ctx.plugin.prompt_tokens = prompt_tokens
        end
    else
        return fail(400, "only support content-type is application/json")
    end
end

function AIStatisticsPluginHandler:body_filter(conf)
    if kong.ctx.plugin.skip then
        return
    end

    local response_status = kong.service.response.get_status()
    if response_status ~= 200 then
        return
    end

    handle_stream_response(conf, ngx.arg[1], ngx.arg[2])
end

function AIStatisticsPluginHandler:header_filter(conf)
    if kong.ctx.plugin.skip then
        return
    end

    local response_status = kong.service.response.get_status()
    if response_status ~= 200 then
        return
    end

    local content_type = kong.service.response.get_header("Content-Type")
    local normalized_content_type = content_type and content_type:sub(1, (content_type:find(";") or 0) - 1)
    if normalized_content_type and normalized_content_type == "text/event-stream" then
        kong.ctx.plugin.sse_body_buffer = buffer.new()
        return true
    end

    handle_json_response(conf)
end

function AIStatisticsPluginHandler:log(conf)
    if kong.ctx.plugin.skip then
        return
    end

    local response_status = kong.service.response.get_status()
    if response_status ~= 200 then
        return
    end

    local meta = {
        plugin_id = conf.__plugin_id,
        request_model = kong.ctx.plugin.request_model,
        response_model = kong.ctx.plugin.response_model,
    }

    kong.log.set_serialize_value("ai.statistics.meta", meta)

    local usage = {
        completion_tokens = kong.ctx.plugin.completions_tokens,
        prompt_tokens = kong.ctx.plugin.prompt_tokens,
        total_tokens = kong.ctx.plugin.total_tokens,
    }

    kong.log.set_serialize_value("ai.statistics.usage",usage)
end

return AIStatisticsPluginHandler