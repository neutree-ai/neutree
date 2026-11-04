local json = require 'cjson.safe'
local ffi = require 'ffi'
ffi.cdef[[int clock_gettime(int, struct timespec *);]]
local function now()
    local t = ffi.new('struct timespec[1]')
    assert(ffi.C.clock_gettime(1, t) == 0)
    return tonumber(t[0].tv_sec) + tonumber(t[0].tv_nsec) / 1e9
end
local function event(kind, fields)
    local ctx = ngx.ctx.rto
    if not ctx then return end
    fields.kind, fields.t = kind, now()
    ctx.events[#ctx.events + 1] = fields
end
local P = { PRIORITY = 1000000, VERSION = '0.1.0' }
function P:init_worker()
    local routing = require 'kong.plugins.neutree-ai-gateway.routing'
    local health = require 'kong.plugins.neutree-ai-gateway.health'
    local select = routing.next
    routing.next = function(state)
        local started = now()
        local target, err, detail = select(state)
        event('select', { started = started, duration_ms = (now() - started) * 1000,
            upstream = target and target.upstream, model = target and target.upstream_model, error = err })
        return target, err, detail
    end
    local record = health.record
    health.record = function(shared, admission, outcome)
        local started = now()
        local ok, err = record(shared, admission, outcome)
        event('health_record', { started = started, duration_ms = (now() - started) * 1000,
            outcome = outcome, probe = admission.probe ~= nil, ok = ok, error = err })
        return ok, err
    end
end
function P:access()
    ngx.ctx.rto = { id = kong.request.get_header('X-Rto-ID'), access = now(), events = {} }
end
function P:header_filter()
    event('response_headers', { status = kong.response.get_status() })
end
function P:log()
    local ctx = ngx.ctx.rto
    if not ctx then return end
    ctx.finished = now()
    ctx.status = kong.response.get_status()
    ctx.upstream_connect_time = ngx.var.upstream_connect_time
    ctx.upstream_header_time = ngx.var.upstream_header_time
    ctx.upstream_response_time = ngx.var.upstream_response_time
    kong.log.notice('RTO_EVENT ', json.encode(ctx))
end
return P
