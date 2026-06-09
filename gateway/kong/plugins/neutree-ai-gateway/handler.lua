local cjson = require("cjson.safe")
local buffer = require("string.buffer")
local ai_shared = require("kong.llm.drivers.shared")
local ai_driver = require("kong.llm.drivers.openai")
local strip = require("kong.tools.string").strip

local AIGatewayHandler = {
    PRIORITY = 1100,
    VERSION = "0.0.1",
}

local EMPTY = {}

-- cjson decodes JSON null as cjson.null (a userdata sentinel).
-- Indexing userdata causes "attempt to index a userdata value".
-- Use this helper wherever a decoded field might be null.
local function is_table(v)
    return type(v) == "table"
end

local function string_or_empty(v)
    return type(v) == "string" and v or ""
end

-- Capture usage breakdown fields (cache / reasoning / cost) from an
-- OpenAI-compatible `usage` object into kong.ctx.plugin so the log phase can
-- forward them to the usage ledger. Only sets values that are actually present.
local function record_extended_usage(ctx, usage)
    if not is_table(usage) then
        return
    end
    local ptd = is_table(usage.prompt_tokens_details) and usage.prompt_tokens_details or EMPTY
    local ctd = is_table(usage.completion_tokens_details) and usage.completion_tokens_details or EMPTY
    if ptd.cached_tokens ~= nil then
        ctx.cache_read_tokens = ptd.cached_tokens
    end
    local cache_creation = ptd.cache_write_tokens or ptd.cache_creation_tokens
    if cache_creation ~= nil then
        ctx.cache_creation_tokens = cache_creation
    end
    if ctd.reasoning_tokens ~= nil then
        ctx.reasoning_tokens = ctd.reasoning_tokens
    end
    if usage.cost ~= nil then
        ctx.cost_usd = usage.cost
    end
end

-- Anthropic reports cache tokens separately from input_tokens, while OpenAI
-- `prompt_tokens` already includes cached tokens. So the Anthropic input_tokens
-- is prompt_tokens minus the cache read/creation portions.
local function anthropic_usage_from_openai(usage)
    usage = is_table(usage) and usage or EMPTY
    local ptd = is_table(usage.prompt_tokens_details) and usage.prompt_tokens_details or EMPTY
    local prompt = usage.prompt_tokens or 0
    local cache_read = ptd.cached_tokens or 0
    local cache_creation = ptd.cache_write_tokens or ptd.cache_creation_tokens or 0
    local input = prompt - cache_read - cache_creation
    if input < 0 then
        -- provider did not include cache tokens inside prompt_tokens; keep raw
        input = prompt
    end
    return {
        input_tokens = input,
        output_tokens = usage.completion_tokens or 0,
        cache_read_input_tokens = cache_read,
        cache_creation_input_tokens = cache_creation,
    }
end

local SUPPORTED_ROUTE_TYPES = {
    "/v1/chat/completions",
    "/v1/embeddings",
    "/v1/rerank",
}

local function extract_suffix(request_path, prefix)
    local suffix = request_path
    if prefix and prefix ~= "" then
        local prefix_pattern = "^" .. prefix:gsub("([%-%.%+%[%]%(%)%$%^%%%?%*])", "%%%1")
        suffix = request_path:gsub(prefix_pattern, "", 1)
    end
    if suffix == "" then
        return "/"
    end
    return suffix
end

-- Strip the /v1 prefix from client-facing suffix so it is not duplicated
-- when the upstream URL already includes the API version path (e.g. /v1).
local function strip_api_version_prefix(path)
    local stripped = path:gsub("^/v1/", "/", 1)
    if stripped == path then
        -- also handle exact "/v1" without trailing slash
        stripped = path:gsub("^/v1$", "/", 1)
    end
    return stripped
end

local function is_models_path(path)
    return path == "/v1/models" or path == "/v1/models/"
end

local function is_anthropic_models_path(path)
    return path == "/anthropic/v1/models" or path == "/anthropic/v1/models/"
end

local function is_anthropic_messages_path(path)
    return path == "/anthropic/v1/messages" or path == "/anthropic/v1/messages/"
end

local function detect_route_type(request_path)
    for _, rt in ipairs(SUPPORTED_ROUTE_TYPES) do
        if string.match(request_path, rt .. "$") then
            return rt
        end
    end
    return nil
end

local function get_token_text(event_t)
    local first_choice = ((event_t or EMPTY).choices or EMPTY)[1] or EMPTY
    local token_text = (first_choice.delta or EMPTY).content or first_choice.text or ""
    return (type(token_text) == "string" and token_text) or ""
end

local function sse_frame(event_type, data)
    if type(data) == "table" then
        data = cjson.encode(data)
    end
    if event_type then
        return "event: " .. event_type .. "\ndata: " .. data .. "\n\n"
    end
    return "data: " .. data .. "\n\n"
end

local function anthropic_error(status, err_type, message)
    return kong.response.exit(status, {
        type = "error",
        error = {
            type = err_type,
            message = message,
        },
    })
end

local function fail(code, msg)
    return kong.response.exit(code, msg and { error = { message = msg } } or nil)
end

local function resolve_upstream(conf, model)
    if not conf.upstreams then
        return nil
    end

    for _, entry in ipairs(conf.upstreams) do
        if entry.model_mapping[model] then
            return entry
        end
    end

    return nil
end

local function set_upstream_target(entry)
    local target_host = entry.host
    local connect_host = target_host
    if not target_host:match("^%d+%.%d+%.%d+%.%d+$") then
        local toip = require("socket").dns.toip
        local ip, err = toip(target_host)
        if not ip then
            return nil, kong.response.exit(502, {
                error = {
                    message = "DNS resolution failed for upstream: " .. target_host .. " (" .. (err or "unknown") .. ")",
                    type = "upstream_error",
                },
            })
        end
        connect_host = ip
    end

    kong.service.set_target(connect_host, entry.port)
    kong.service.request.set_scheme(entry.scheme)

    if connect_host ~= target_host then
        kong.service.request.set_header("Host", target_host)
        ngx.var.upstream_host = target_host
    end

    -- Clear Kong authentication headers to prevent leaking to upstream
    kong.service.request.clear_header("x-consumer-id")
    kong.service.request.clear_header("x-consumer-custom-id")
    kong.service.request.clear_header("x-consumer-username")
    kong.service.request.clear_header("x-credential-identifier")
    kong.service.request.clear_header("x-anonymous-consumer")

    if entry.auth_header and entry.auth_header ~= "" then
        kong.service.request.set_header("Authorization", entry.auth_header)
    end

    return connect_host
end

local function build_upstream_path(entry, api_path)
    local upstream_base = entry.path or "/"
    if upstream_base == "/" then
        upstream_base = ""
    end
    upstream_base = upstream_base:gsub("/$", "")

    local final_path = upstream_base .. api_path
    if final_path == "" then
        final_path = "/"
    end
    return final_path
end

local function maybe_return_model_list(conf, suffix)
    if not conf.upstreams then
        return false
    end

    if is_models_path(suffix) and kong.request.get_method() == "GET" then
        local models = {}
        for _, entry in ipairs(conf.upstreams) do
            for model_name, _ in pairs(entry.model_mapping) do
                models[#models + 1] = {
                    id = model_name,
                    object = "model",
                    created = 0,
                    owned_by = "external-endpoint",
                }
            end
        end
        kong.response.exit(200, {
            object = "list",
            data = models,
        })
        return true
    end

    if is_anthropic_models_path(suffix) and kong.request.get_method() == "GET" then
        local models = {}
        for _, entry in ipairs(conf.upstreams) do
            for model_name, _ in pairs(entry.model_mapping) do
                models[#models + 1] = {
                    id = model_name,
                    type = "model",
                    display_name = model_name,
                    created_at = "2025-01-01T00:00:00Z",
                }
            end
        end
        kong.response.exit(200, {
            data = models,
            has_more = false,
            first_id = models[1] and models[1].id or nil,
            last_id = models[#models] and models[#models].id or nil,
        })
        return true
    end

    return false
end

local function map_finish_reason(reason)
    if reason == "stop" then return "end_turn" end
    if reason == "length" then return "max_tokens" end
    if reason == "tool_calls" then return "tool_use" end
    return "end_turn"
end

local function convert_tool_choice(tc)
    if tc == nil then
        return nil
    end
    if type(tc) == "string" then
        if tc == "auto" then return "auto" end
        if tc == "any" then return "required" end
        if tc == "none" then return "none" end
        return tc
    end
    if type(tc) == "table" then
        if tc.type == "auto" then return "auto" end
        if tc.type == "any" then return "required" end
        if tc.type == "tool" then
            return { type = "function", ["function"] = { name = tc.name } }
        end
    end
    return nil
end

local function convert_tools(tools)
    if not is_table(tools) then return nil end
    local result = {}
    for i, tool in ipairs(tools) do
        result[i] = {
            type = "function",
            ["function"] = {
                name = tool.name,
                description = tool.description,
                parameters = tool.input_schema,
            },
        }
    end
    -- An empty Lua table serialises to a JSON object `{}`, which upstreams
    -- (e.g. vLLM) reject for the array-typed `tools` field. Omit `tools`
    -- entirely when the client sent an empty list.
    if #result == 0 then return nil end
    return result
end

local function append_system_content(parts, content)
    if type(content) == "string" then
        if content ~= "" then
            parts[#parts + 1] = content
        end
    elseif type(content) == "table" then
        for _, block in ipairs(content) do
            if type(block) == "table" and type(block.text) == "string" and block.text ~= "" then
                parts[#parts + 1] = block.text
            elseif type(block) == "string" and block ~= "" then
                parts[#parts + 1] = block
            end
        end
    end
end

local function convert_messages(anthropic_messages)
    local openai_messages = {}
    local system_parts = {}

    for _, msg in ipairs(anthropic_messages) do
        local role = msg.role
        if role == "ctx" or role == "msg" then
            role = "user"
        end

        if role == "user" then
            if type(msg.content) == "string" then
                openai_messages[#openai_messages + 1] = {
                    role = "user",
                    content = msg.content,
                }
            elseif type(msg.content) == "table" then
                local user_parts = {}
                local tool_results = {}
                local trailing_user_parts = {}
                local found_tool_result = false

                for _, block in ipairs(msg.content) do
                    if block.type == "tool_result" then
                        found_tool_result = true
                        local tool_content = ""
                        if type(block.content) == "string" then
                            tool_content = block.content
                        elseif type(block.content) == "table" then
                            local parts = {}
                            for _, cb in ipairs(block.content) do
                                if cb.type == "text" then
                                    parts[#parts + 1] = cb.text or ""
                                end
                            end
                            tool_content = table.concat(parts, "\n")
                        end

                        if block.is_error then
                            tool_content = "Error: " .. tool_content
                        end

                        tool_results[#tool_results + 1] = {
                            role = "tool",
                            tool_call_id = block.tool_use_id,
                            content = tool_content,
                        }
                    elseif found_tool_result then
                        if block.type == "text" then
                            trailing_user_parts[#trailing_user_parts + 1] = {
                                type = "text",
                                text = block.text,
                            }
                        elseif block.type == "image" then
                            local url = "data:" .. (block.source.media_type or "image/png") .. ";base64," .. block.source.data
                            trailing_user_parts[#trailing_user_parts + 1] = {
                                type = "image_url",
                                image_url = { url = url },
                            }
                        end
                    else
                        if block.type == "text" then
                            user_parts[#user_parts + 1] = {
                                type = "text",
                                text = block.text,
                            }
                        elseif block.type == "image" then
                            local url = "data:" .. (block.source.media_type or "image/png") .. ";base64," .. block.source.data
                            user_parts[#user_parts + 1] = {
                                type = "image_url",
                                image_url = { url = url },
                            }
                        end
                    end
                end

                if #user_parts > 0 then
                    openai_messages[#openai_messages + 1] = {
                        role = "user",
                        content = user_parts,
                    }
                end

                for _, tr in ipairs(tool_results) do
                    openai_messages[#openai_messages + 1] = tr
                end

                if #trailing_user_parts > 0 then
                    if #trailing_user_parts == 1 and trailing_user_parts[1].type == "text" then
                        openai_messages[#openai_messages + 1] = {
                            role = "user",
                            content = trailing_user_parts[1].text,
                        }
                    else
                        openai_messages[#openai_messages + 1] = {
                            role = "user",
                            content = trailing_user_parts,
                        }
                    end
                end

                if #user_parts == 0 and #tool_results == 0 and #trailing_user_parts == 0 then
                    openai_messages[#openai_messages + 1] = {
                        role = "user",
                        content = msg.content,
                    }
                end
            end
        elseif role == "assistant" then
            if type(msg.content) == "string" then
                openai_messages[#openai_messages + 1] = {
                    role = "assistant",
                    content = msg.content,
                }
            elseif type(msg.content) == "table" then
                local text_parts = {}
                local tool_calls = {}

                for _, block in ipairs(msg.content) do
                    if block.type == "text" then
                        text_parts[#text_parts + 1] = block.text
                    elseif block.type == "tool_use" then
                        local args_str = ""
                        if block.input then
                            args_str = cjson.encode(block.input)
                        end
                        tool_calls[#tool_calls + 1] = {
                            id = block.id,
                            type = "function",
                            ["function"] = {
                                name = block.name,
                                arguments = args_str,
                            },
                        }
                    end
                end

                local assistant_msg = { role = "assistant" }
                local combined_text = table.concat(text_parts, "\n\n")
                if combined_text ~= "" then
                    assistant_msg.content = combined_text
                end
                if #tool_calls > 0 then
                    assistant_msg.tool_calls = tool_calls
                end
                openai_messages[#openai_messages + 1] = assistant_msg
            end
        elseif role == "system" then
            append_system_content(system_parts, msg.content)
        else
            openai_messages[#openai_messages + 1] = msg
        end
    end

    return openai_messages, system_parts
end

local function convert_request(anthropic_req)
    local openai_req = {
        model = anthropic_req.model,
        max_tokens = anthropic_req.max_tokens,
        stream = anthropic_req.stream,
        temperature = anthropic_req.temperature,
        top_p = anthropic_req.top_p,
    }

    local messages = {}
    local system_parts = {}
    if anthropic_req.system then
        append_system_content(system_parts, anthropic_req.system)
    end

    local converted, inline_system_parts = convert_messages(anthropic_req.messages or {})
    for _, part in ipairs(inline_system_parts) do
        system_parts[#system_parts + 1] = part
    end
    if #system_parts > 0 then
        messages[#messages + 1] = { role = "system", content = table.concat(system_parts, "\n\n") }
    end
    for _, m in ipairs(converted) do
        messages[#messages + 1] = m
    end
    openai_req.messages = messages

    if anthropic_req.stop_sequences then
        openai_req.stop = anthropic_req.stop_sequences
    end
    if anthropic_req.stream then
        openai_req.stream_options = { include_usage = true }
    end
    if anthropic_req.tools then
        openai_req.tools = convert_tools(anthropic_req.tools)
    end
    if anthropic_req.tool_choice then
        openai_req.tool_choice = convert_tool_choice(anthropic_req.tool_choice)
    end

    return openai_req
end

local function convert_response(openai_resp, request_model)
    local choice = ((openai_resp.choices or EMPTY)[1]) or EMPTY
    local message = choice.message or EMPTY
    local content = {}

    -- Surface the model's reasoning as an Anthropic thinking block (OpenRouter
    -- uses message.reasoning, vLLM/DeepSeek use message.reasoning_content)
    -- instead of dropping it. Thinking blocks precede the text block.
    local reasoning = message.reasoning_content
    if reasoning == nil or reasoning == cjson.null then
        reasoning = message.reasoning
    end
    if type(reasoning) == "string" and reasoning ~= "" then
        content[#content + 1] = {
            type = "thinking",
            thinking = reasoning,
            signature = "",
        }
    end

    if message.content and message.content ~= "" and message.content ~= cjson.null then
        content[#content + 1] = {
            type = "text",
            text = message.content,
        }
    end

    if is_table(message.tool_calls) then
        for _, tc in ipairs(message.tool_calls) do
            local input = {}
            -- `tc["function"]` may be cjson.null (a truthy userdata), so guard
            -- with is_table before indexing it (NEU-404).
            local fn = is_table(tc["function"]) and tc["function"] or EMPTY
            local args = fn.arguments
            if type(args) == "string" then
                local parsed, err = cjson.decode(args)
                if err or parsed == nil then
                    input = { raw = args }
                else
                    input = parsed
                end
            end
            content[#content + 1] = {
                type = "tool_use",
                id = tc.id,
                name = string_or_empty(fn.name),
                input = input,
            }
        end
    end

    if #content == 0 then
        content[#content + 1] = {
            type = "text",
            text = "",
        }
    end

    return {
        id = openai_resp.id or ("msg_" .. ngx.now()),
        type = "message",
        role = "assistant",
        model = request_model or openai_resp.model or "unknown",
        content = content,
        stop_reason = map_finish_reason(choice.finish_reason),
        stop_sequence = cjson.null,
        usage = anthropic_usage_from_openai(openai_resp.usage),
    }
end

local function make_message_start(request_model, input_tokens)
    return {
        type = "message_start",
        message = {
            id = "msg_" .. ngx.now(),
            type = "message",
            role = "assistant",
            model = request_model or "unknown",
            content = {},
            stop_reason = cjson.null,
            stop_sequence = cjson.null,
            usage = {
                input_tokens = input_tokens or 0,
                output_tokens = 0,
                cache_creation_input_tokens = 0,
                cache_read_input_tokens = 0,
            },
        },
    }
end

local function make_content_block_start_text(index)
    return {
        type = "content_block_start",
        index = index,
        content_block = {
            type = "text",
            text = "",
        },
    }
end

local function make_content_block_start_tool_use(index, id, name)
    return {
        type = "content_block_start",
        index = index,
        content_block = {
            type = "tool_use",
            id = id,
            name = name,
            input = {},
        },
    }
end

local function make_content_block_start_thinking(index)
    return {
        type = "content_block_start",
        index = index,
        content_block = {
            type = "thinking",
            thinking = "",
            signature = "",
        },
    }
end

local function make_content_block_delta_text(index, text)
    return {
        type = "content_block_delta",
        index = index,
        delta = {
            type = "text_delta",
            text = text,
        },
    }
end

local function make_content_block_delta_thinking(index, thinking)
    return {
        type = "content_block_delta",
        index = index,
        delta = {
            type = "thinking_delta",
            thinking = thinking,
        },
    }
end

local function make_content_block_delta_signature(index, signature)
    return {
        type = "content_block_delta",
        index = index,
        delta = {
            type = "signature_delta",
            signature = signature,
        },
    }
end

local function make_content_block_delta_json(index, partial_json)
    return {
        type = "content_block_delta",
        index = index,
        delta = {
            type = "input_json_delta",
            partial_json = partial_json,
        },
    }
end

local function make_content_block_stop(index)
    return {
        type = "content_block_stop",
        index = index,
    }
end

local function make_ping()
    return { type = "ping" }
end

-- usage is the full Anthropic usage table (input/output/cache). Anthropic
-- repeats the cumulative usage on the terminal message_delta, so we forward the
-- whole object rather than output_tokens alone.
local function make_message_delta(stop_reason, usage)
    return {
        type = "message_delta",
        delta = {
            stop_reason = stop_reason or "end_turn",
            stop_sequence = cjson.null,
        },
        usage = usage or { output_tokens = 0 },
    }
end

local function make_message_stop()
    return { type = "message_stop" }
end

local function handle_openai_json_response()
    local response_body = kong.service.response.get_raw_body()
    if response_body == nil then
        return
    end

    kong.ctx.plugin.response_body_raw = response_body

    local ai_response, err = cjson.decode(response_body)
    if err then
        return
    end

    local route_type = kong.ctx.plugin.route_type
    kong.ctx.plugin.response_model = ai_response.model
    local first_choice = ((ai_response.choices or EMPTY)[1]) or EMPTY
    if first_choice.finish_reason and first_choice.finish_reason ~= cjson.null then
        kong.ctx.plugin.finish_reason = first_choice.finish_reason
    end
    if ai_response.id ~= nil then
        kong.ctx.plugin.message_id = ai_response.id
    end
    if is_table(ai_response.usage) then
        if route_type == "/v1/chat/completions" then
            kong.ctx.plugin.completions_tokens = ai_response.usage.completion_tokens
            kong.ctx.plugin.prompt_tokens = ai_response.usage.prompt_tokens
            kong.ctx.plugin.total_tokens = ai_response.usage.total_tokens
        else
            kong.ctx.plugin.prompt_tokens = ai_response.usage.prompt_tokens
            kong.ctx.plugin.total_tokens = ai_response.usage.total_tokens
        end
        record_extended_usage(kong.ctx.plugin, ai_response.usage)
    end
end

local function handle_openai_stream_response(chunk, finished)
    local content_type = kong.service.response.get_header("Content-Type")
    local normalized_content_type = content_type and content_type:sub(1, (content_type:find(";") or 0) - 1)
    if normalized_content_type and normalized_content_type ~= "text/event-stream" then
        return
    end

    local body_buffer = kong.ctx.plugin.sse_body_buffer
    local raw_buffer = kong.ctx.plugin.sse_raw_buffer
    if raw_buffer and type(chunk) == "string" and chunk ~= "" then
        raw_buffer:put(chunk)
    end
    if type(chunk) == "string" and chunk ~= "" then
        local events = ai_shared.frame_to_events(chunk, normalized_content_type)
        if not events then
            return
        end

        for _, event in ipairs(events) do
            local formatted = ai_driver.from_format(event, "", "stream/llm/v1/chat")
            if formatted and formatted ~= ai_shared._CONST.SSE_TERMINATOR then
                local event_t, err = cjson.decode(formatted)
                if not err then
                    if event_t.choices and #event_t.choices > 0 and body_buffer ~= nil then
                        body_buffer:put(get_token_text(event_t))
                    end
                    if event_t.choices and event_t.choices[1] and event_t.choices[1].finish_reason
                        and event_t.choices[1].finish_reason ~= cjson.null then
                        kong.ctx.plugin.finish_reason = event_t.choices[1].finish_reason
                    end
                    if kong.ctx.plugin.response_model == nil and event_t.model then
                        kong.ctx.plugin.response_model = event_t.model
                    end
                    if event_t.id ~= nil then
                        kong.ctx.plugin.message_id = event_t.id
                    end
                    if is_table(event_t.usage) then
                        kong.ctx.plugin.prompt_tokens = event_t.usage.prompt_tokens
                        kong.ctx.plugin.completions_tokens = event_t.usage.completion_tokens
                        kong.ctx.plugin.total_tokens = event_t.usage.total_tokens
                        record_extended_usage(kong.ctx.plugin, event_t.usage)
                    end
                end
            end
        end
    end

    if finished and kong.ctx.plugin.total_tokens == nil and body_buffer ~= nil then
        local response = body_buffer:get()
        local completion_tokens_count = math.ceil(#strip(response) / 4)
        kong.ctx.plugin.completions_tokens = completion_tokens_count
        kong.ctx.plugin.total_tokens = kong.ctx.plugin.completions_tokens + (kong.ctx.plugin.prompt_tokens or 0)
    end
end

local function handle_anthropic_non_stream_body()
    local ctx = kong.ctx.plugin
    if not ctx.body_buffer then
        ctx.body_buffer = ""
    end
    ctx.body_buffer = ctx.body_buffer .. (ngx.arg[1] or "")

    if ngx.arg[2] then
        ctx.response_body_raw = ctx.body_buffer

        local openai_resp, err = cjson.decode(ctx.body_buffer)
        if err or openai_resp == nil then
            ngx.arg[1] = ctx.body_buffer
            return
        end

        ctx.response_model = openai_resp.model
        local first_choice = ((openai_resp.choices or EMPTY)[1]) or EMPTY
        if first_choice.finish_reason and first_choice.finish_reason ~= cjson.null then
            ctx.finish_reason = first_choice.finish_reason
        end
        if openai_resp.id ~= nil then
            ctx.message_id = openai_resp.id
        end
        if is_table(openai_resp.usage) then
            ctx.input_tokens = openai_resp.usage.prompt_tokens or 0
            ctx.output_tokens = openai_resp.usage.completion_tokens or 0
            record_extended_usage(ctx, openai_resp.usage)
        end

        local anthropic_resp = convert_response(openai_resp, ctx.request_model)
        ngx.arg[1] = cjson.encode(anthropic_resp)
    else
        ngx.arg[1] = ""
    end
end

-- Build the Anthropic usage table for the terminal message_delta from whatever
-- upstream usage we captured. Anthropic input_tokens excludes the cache tokens
-- (OpenAI folds them into prompt_tokens), matching anthropic_usage_from_openai
-- on the non-streaming path. When the upstream never sent a usage block, fall
-- back to the access-phase prompt estimate and a char-based completion estimate
-- so the client still sees non-zero, plausible numbers.
local function anthropic_stream_final_usage(ctx)
    local cache_read = ctx.cache_read_tokens or 0
    local cache_creation = ctx.cache_creation_tokens or 0
    local input, output
    if ctx.usage_seen then
        input = (ctx.input_tokens or 0) - cache_read - cache_creation
        if input < 0 then
            input = ctx.input_tokens or 0
        end
        output = ctx.output_tokens or 0
        if output == 0 and (ctx.output_chars or 0) > 0 then
            output = math.ceil(ctx.output_chars / 4)
        end
    else
        input = ctx.input_tokens_estimate or 0
        output = math.ceil((ctx.output_chars or 0) / 4)
    end
    return {
        input_tokens = input,
        output_tokens = output,
        cache_read_input_tokens = cache_read,
        cache_creation_input_tokens = cache_creation,
    }
end

local function handle_anthropic_stream_body()
    local ctx = kong.ctx.plugin
    local chunk = ngx.arg[1] or ""
    local is_last = ngx.arg[2]
    ctx.sse_buffer = (ctx.sse_buffer or "") .. chunk
    -- The anthropic streaming path never reaches the SSE buffer set up in
    -- header_filter, so accumulate the raw upstream response here for tracing.
    ctx.response_body_raw = (ctx.response_body_raw or "") .. chunk

    local output_parts = {}

    -- message_start + ping are emitted lazily on the first content delta so the
    -- per-block structure can adapt to thinking / text / tool_use ordering.
    local function ensure_started()
        if ctx.message_started then
            return
        end
        output_parts[#output_parts + 1] = sse_frame("message_start", make_message_start(ctx.request_model, ctx.input_tokens_estimate))
        output_parts[#output_parts + 1] = sse_frame("ping", make_ping())
        ctx.message_started = true
    end

    -- Close the currently open content block (if any). Thinking blocks flush a
    -- trailing signature_delta first when the upstream provided a signature.
    local function close_block()
        if ctx.open_block_type == nil then
            return
        end
        if ctx.open_block_type == "thinking" and type(ctx.pending_signature) == "string"
            and ctx.pending_signature ~= "" then
            output_parts[#output_parts + 1] = sse_frame("content_block_delta", make_content_block_delta_signature(ctx.anthropic_block_index, ctx.pending_signature))
        end
        ctx.pending_signature = nil
        output_parts[#output_parts + 1] = sse_frame("content_block_stop", make_content_block_stop(ctx.anthropic_block_index))
        ctx.open_block_type = nil
    end

    -- Close any open block and allocate the next block index for block_type.
    local function open_block(block_type)
        close_block()
        local idx = ctx.next_block_index or 0
        ctx.next_block_index = idx + 1
        ctx.anthropic_block_index = idx
        ctx.open_block_type = block_type
        return idx
    end

    local function finish_stream(stop_reason)
        if ctx.finish_events_sent or not ctx.message_started then
            return
        end
        close_block()
        output_parts[#output_parts + 1] = sse_frame("message_delta", make_message_delta(stop_reason or map_finish_reason(ctx.finish_reason), anthropic_stream_final_usage(ctx)))
        output_parts[#output_parts + 1] = sse_frame("message_stop", make_message_stop())
        ctx.finish_events_sent = true
    end

    while true do
        local event_end = string.find(ctx.sse_buffer, "\n\n", 1, true)
        if not event_end then
            break
        end

        local raw_event = string.sub(ctx.sse_buffer, 1, event_end - 1)
        ctx.sse_buffer = string.sub(ctx.sse_buffer, event_end + 2)

        local data_line = nil
        for line in string.gmatch(raw_event, "[^\n]+") do
            local d = string.match(line, "^data:%s*(.+)")
            if d then
                data_line = d
            end
        end

        if data_line == nil then
            goto continue
        end

        if data_line == "[DONE]" then
            -- By the time [DONE] arrives the trailing usage-only chunk has
            -- already been processed, so finish_stream sees the final usage.
            finish_stream(map_finish_reason(ctx.finish_reason))
            output_parts[#output_parts + 1] = "data: [DONE]\n\n"
            ctx.done_emitted = true
            goto continue
        end

        local event_t, parse_err = cjson.decode(data_line)
        if parse_err or event_t == nil then
            goto continue
        end

        if event_t.model and ctx.response_model == nil then
            ctx.response_model = event_t.model
        end
        if event_t.id ~= nil then
            ctx.message_id = event_t.id
        end
        if is_table(event_t.usage) then
            ctx.input_tokens = event_t.usage.prompt_tokens or ctx.input_tokens
            ctx.output_tokens = event_t.usage.completion_tokens or ctx.output_tokens
            ctx.usage_seen = true
            record_extended_usage(ctx, event_t.usage)
        end

        local choice = ((event_t.choices or EMPTY)[1]) or nil
        if choice == nil then
            goto continue
        end

        local delta = choice.delta or EMPTY
        local finish_reason = choice.finish_reason
        if finish_reason and finish_reason ~= cjson.null then
            ctx.finish_reason = finish_reason
        end

        -- Reasoning / thinking: OpenRouter uses delta.reasoning, vLLM/DeepSeek
        -- use delta.reasoning_content. Emit an Anthropic thinking block so the
        -- model's chain-of-thought is not silently dropped.
        local reasoning = delta.reasoning_content
        if reasoning == nil or reasoning == cjson.null then
            reasoning = delta.reasoning
        end
        if type(reasoning) == "string" and reasoning ~= "" then
            ensure_started()
            if ctx.open_block_type ~= "thinking" then
                local idx = open_block("thinking")
                output_parts[#output_parts + 1] = sse_frame("content_block_start", make_content_block_start_thinking(idx))
            end
            output_parts[#output_parts + 1] = sse_frame("content_block_delta", make_content_block_delta_thinking(ctx.anthropic_block_index, reasoning))
            ctx.output_chars = (ctx.output_chars or 0) + #reasoning
        end
        -- Capture a reasoning signature if the upstream supplies one; it is
        -- flushed as a signature_delta when the thinking block closes.
        if is_table(delta.reasoning_details) then
            for _, rd in ipairs(delta.reasoning_details) do
                if is_table(rd) and type(rd.signature) == "string" and rd.signature ~= "" then
                    ctx.pending_signature = rd.signature
                end
            end
        end

        if type(delta.content) == "string" and delta.content ~= "" then
            ensure_started()
            if ctx.open_block_type ~= "text" then
                local idx = open_block("text")
                output_parts[#output_parts + 1] = sse_frame("content_block_start", make_content_block_start_text(idx))
            end
            output_parts[#output_parts + 1] = sse_frame("content_block_delta", make_content_block_delta_text(ctx.anthropic_block_index, delta.content))
            ctx.output_chars = (ctx.output_chars or 0) + #delta.content
        end

        if is_table(delta.tool_calls) then
            ensure_started()
            for _, tc in ipairs(delta.tool_calls) do
                -- `tc["function"]` may be cjson.null (a truthy userdata), so
                -- guard with is_table before indexing it (NEU-404).
                local fn = is_table(tc["function"]) and tc["function"] or EMPTY
                local tc_index = tc.index or 0
                if ctx.open_block_type ~= "tool_use" or tc_index ~= ctx.current_tool_index then
                    local idx = open_block("tool_use")
                    ctx.current_tool_index = tc_index
                    local tc_id = tc.id or ""
                    -- Coerce the name through string_or_empty so nil/cjson.null
                    -- collapse to "" and never leak a non-string into the SSE
                    -- frame (which would break JSON clients).
                    local tc_name = string_or_empty(fn.name)
                    output_parts[#output_parts + 1] = sse_frame("content_block_start", make_content_block_start_tool_use(idx, tc_id, tc_name))
                end

                local args = fn.arguments
                if type(args) == "string" and args ~= "" then
                    output_parts[#output_parts + 1] = sse_frame("content_block_delta", make_content_block_delta_json(ctx.anthropic_block_index, args))
                end
            end
        end

        -- Do NOT emit the terminal events on finish_reason: the upstream sends
        -- its usage in a separate trailing chunk after the finish_reason chunk,
        -- so deferring to [DONE] / is_last lets message_delta carry real tokens.

        ::continue::
    end

    if is_last then
        finish_stream(map_finish_reason(ctx.finish_reason))
        if not ctx.done_emitted then
            output_parts[#output_parts + 1] = "data: [DONE]\n\n"
            ctx.done_emitted = true
        end
    end

    ngx.arg[1] = table.concat(output_parts)
end

function AIGatewayHandler:access(conf)
    local request_path = kong.request.get_path()
    local suffix = extract_suffix(request_path, conf.route_prefix or "")

    if maybe_return_model_list(conf, suffix) then
        return
    end

    if is_anthropic_messages_path(suffix) then
        local content_type = kong.request.get_header("Content-Type") or "application/json"
        if not string.find(content_type, "application/json", nil, true) then
            return anthropic_error(400, "invalid_request_error", "Content-Type must be application/json")
        end

        local request_body = kong.request.get_raw_body()
        if request_body == nil or request_body == "" then
            return anthropic_error(400, "invalid_request_error", "Request body is required")
        end

        kong.ctx.plugin.request_body_raw = request_body

        local anthropic_req, err = cjson.decode(request_body)
        if err or anthropic_req == nil then
            return anthropic_error(400, "invalid_request_error", "Invalid JSON in request body")
        end

        local openai_req = convert_request(anthropic_req)
        kong.ctx.plugin.anthropic_mode = true
        kong.ctx.plugin.request_model = anthropic_req.model
        kong.ctx.plugin.response_model = nil
        kong.ctx.plugin.is_stream = anthropic_req.stream == true
        kong.ctx.plugin.route_type = "/v1/chat/completions"

        if conf.upstreams then
            local matched_entry = resolve_upstream(conf, openai_req.model)
            if not matched_entry then
                return anthropic_error(400, "invalid_request_error", "No upstream configured for model: " .. tostring(openai_req.model))
            end

            local _, route_err = set_upstream_target(matched_entry)
            if route_err then
                return route_err
            end
            if matched_entry.internal then
                kong.service.request.set_path(build_upstream_path(matched_entry, "/v1/chat/completions"))
            else
                kong.service.request.set_path(build_upstream_path(matched_entry, "/chat/completions"))
            end
            openai_req.model = matched_entry.model_mapping[openai_req.model]
        else
            kong.service.request.set_path(string.gsub(request_path, "/anthropic/v1/messages/?$", "/v1/chat/completions"))
        end

        local openai_json = cjson.encode(openai_req)
        kong.service.request.set_raw_body(openai_json)
        kong.service.request.set_header("Content-Type", "application/json")
        kong.service.request.clear_header("anthropic-version")
        kong.service.request.clear_header("anthropic-beta")

        if not kong.ctx.plugin.is_stream then
            kong.service.request.enable_buffering()
        else
            kong.ctx.plugin.message_started = false
            kong.ctx.plugin.open_block_type = nil
            kong.ctx.plugin.current_tool_index = nil
            kong.ctx.plugin.next_block_index = 0
            kong.ctx.plugin.anthropic_block_index = 0
            kong.ctx.plugin.pending_signature = nil
            kong.ctx.plugin.sse_buffer = ""
            kong.ctx.plugin.input_tokens = 0
            kong.ctx.plugin.output_tokens = 0
            kong.ctx.plugin.output_chars = 0
            kong.ctx.plugin.usage_seen = false
            kong.ctx.plugin.finish_events_sent = false
            kong.ctx.plugin.done_emitted = false
            -- Streaming never returns usage until the trailing chunk, so the
            -- message_start input_tokens can only be a best-effort estimate
            -- (Anthropic emits input_tokens up front; we approximate it here).
            local est, est_err = ai_shared.calculate_cost(openai_req or {}, {}, 1.0)
            kong.ctx.plugin.input_tokens_estimate = (not est_err and est) or 0
        end

        return
    end

    local route_type
    if conf.route_type and type(conf.route_type) == "string" then
        if not string.match(request_path, conf.route_type .. "$") then
            kong.ctx.plugin.skip = true
            return
        end
        route_type = conf.route_type
    else
        route_type = detect_route_type(request_path)
        if not route_type then
            kong.ctx.plugin.skip = true
            return
        end
    end

    kong.ctx.plugin.route_type = route_type

    local content_type = kong.request.get_header("Content-Type") or "application/json"
    if not string.find(content_type, "application/json", nil, true) then
        return fail(400, "only support content-type is application/json")
    end

    local request_body = kong.request.get_raw_body()
    if request_body == nil then
        return fail(400, "request body can not be null")
    end

    kong.ctx.plugin.request_body_raw = request_body

    local ai_request, err = cjson.decode(request_body)
    if err then
        return fail(400, "request body is not json format")
    end

    kong.ctx.plugin.request_model = ai_request.model
    kong.ctx.plugin.is_stream = ai_request.stream == true

    if conf.upstreams then
        if not ai_request.model or ai_request.model == "" then
            return fail(400, "missing 'model' field in request body")
        end

        local matched_entry = resolve_upstream(conf, ai_request.model)
        if not matched_entry then
            return fail(400, "No upstream configured for model: " .. ai_request.model)
        end

        local _, route_err = set_upstream_target(matched_entry)
        if route_err then
            return route_err
        end

        if matched_entry.internal then
            kong.service.request.set_path(build_upstream_path(matched_entry, suffix))
        else
            kong.service.request.set_path(build_upstream_path(matched_entry, strip_api_version_prefix(suffix)))
        end
        ai_request.model = matched_entry.model_mapping[ai_request.model]
        local new_body = cjson.encode(ai_request)
        if new_body then
            kong.service.request.set_raw_body(new_body)
        end
    end

    if not ai_request.stream then
        kong.service.request.enable_buffering()
    else
        local prompt_tokens, cost_err = ai_shared.calculate_cost(ai_request or {}, {}, 1.0)
        if cost_err then
            kong.log.err("unable to estimate request token cost: ", cost_err)
            return fail(500)
        end
        kong.ctx.plugin.prompt_tokens = prompt_tokens
    end
end

function AIGatewayHandler:header_filter(conf)
    if kong.ctx.plugin.skip then
        return
    end

    local response_status = kong.service.response.get_status()
    if response_status ~= 200 then
        return
    end

    if kong.ctx.plugin.anthropic_mode then
        if kong.ctx.plugin.is_stream then
            kong.response.set_header("Content-Type", "text/event-stream")
            kong.response.clear_header("Content-Length")
        else
            kong.response.clear_header("Content-Length")
            kong.response.set_header("Content-Type", "application/json")
        end
        return
    end

    local content_type = kong.service.response.get_header("Content-Type")
    local normalized_content_type = content_type and content_type:sub(1, (content_type:find(";") or 0) - 1)
    if normalized_content_type and normalized_content_type == "text/event-stream" then
        kong.ctx.plugin.sse_body_buffer = buffer.new()
        kong.ctx.plugin.sse_raw_buffer = buffer.new()
        return true
    end

    handle_openai_json_response()
end

function AIGatewayHandler:body_filter(conf)
    if kong.ctx.plugin.skip then
        return
    end

    local response_status = kong.service.response.get_status()
    if response_status ~= 200 then
        -- Error responses bypass the transform paths below, and streaming
        -- requests have no response buffering — so capture the raw error body
        -- here for the log phase to surface.
        local ctx = kong.ctx.plugin
        ctx.response_body_raw = (ctx.response_body_raw or "") .. (ngx.arg[1] or "")
        return
    end

    if kong.ctx.plugin.anthropic_mode then
        if kong.ctx.plugin.is_stream then
            handle_anthropic_stream_body()
        else
            handle_anthropic_non_stream_body()
        end
        return
    end

    handle_openai_stream_response(ngx.arg[1], ngx.arg[2])
end

function AIGatewayHandler:log(conf)
    if kong.ctx.plugin.skip then
        return
    end

    local response_status = kong.service.response.get_status()

    -- Emit raw req/res trace for every request (incl. failures).
    local response_body = kong.ctx.plugin.response_body_raw
    if response_body == nil and kong.ctx.plugin.sse_raw_buffer ~= nil then
        response_body = kong.ctx.plugin.sse_raw_buffer:get()
    end
    if response_body == nil then
        local ok, body = pcall(kong.service.response.get_raw_body)
        if ok and body ~= nil then
            response_body = body
        end
    end
    if kong.ctx.plugin.request_body_raw ~= nil then
        kong.log.set_serialize_value("ai.trace.request_body", kong.ctx.plugin.request_body_raw)
    end
    if response_body ~= nil then
        kong.log.set_serialize_value("ai.trace.response_body", response_body)
    end
    if kong.ctx.plugin.finish_reason ~= nil then
        kong.log.set_serialize_value("ai.trace.finish_reason", kong.ctx.plugin.finish_reason)
    end
    -- Whether the client requested a streaming response.
    kong.log.set_serialize_value("ai.trace.stream", kong.ctx.plugin.is_stream == true)

    if response_status ~= 200 then
        return
    end

    local meta = {
        plugin_id = conf.__plugin_id,
        request_model = kong.ctx.plugin.request_model,
        response_model = kong.ctx.plugin.response_model,
        message_id = kong.ctx.plugin.message_id,
    }
    kong.log.set_serialize_value("ai.statistics.meta", meta)

    local usage
    if kong.ctx.plugin.anthropic_mode then
        usage = {
            completion_tokens = kong.ctx.plugin.output_tokens or 0,
            prompt_tokens = kong.ctx.plugin.input_tokens or 0,
            total_tokens = (kong.ctx.plugin.input_tokens or 0) + (kong.ctx.plugin.output_tokens or 0),
        }
    else
        usage = {
            completion_tokens = kong.ctx.plugin.completions_tokens,
            prompt_tokens = kong.ctx.plugin.prompt_tokens,
            total_tokens = kong.ctx.plugin.total_tokens,
        }
    end
    -- Usage breakdown fields (nil when upstream did not provide them).
    usage.cache_read_tokens = kong.ctx.plugin.cache_read_tokens
    usage.cache_creation_tokens = kong.ctx.plugin.cache_creation_tokens
    usage.reasoning_tokens = kong.ctx.plugin.reasoning_tokens
    usage.cost = kong.ctx.plugin.cost_usd
    kong.log.set_serialize_value("ai.statistics.usage", usage)
end

return AIGatewayHandler
