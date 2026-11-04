local spec_dir = (debug.getinfo(1, "S").source:sub(2)):match("(.*/)") or "./"
local routing = assert(loadfile(spec_dir .. "../routing.lua"))()

describe("passive target failover", function()
    local now, data, expires, dict, conf, original_ngx, original_kong, original_uuid, serial, timers

    local function start()
        local request = assert(routing.begin(conf, "chat", { shared = dict }))
        local target, err = routing.next(request)
        return request, target and target.upstream, err
    end

    local function complete(request, outcome)
        routing.observe(request, outcome)
        routing.finish(request)
    end

    before_each(function()
        original_ngx, original_kong = _G.ngx, _G.kong
        original_uuid = package.loaded["kong.tools.uuid"]
        now, data, expires, serial, timers = 0, {}, {}, 0, {}
        package.loaded["kong.tools.uuid"] = { uuid = function() serial = serial + 1; return tostring(serial) end }
        dict = {
            get = function(_, key)
                if expires[key] and expires[key] <= now then data[key], expires[key] = nil, nil end
                return data[key]
            end,
            safe_set = function(_, key, value, ttl)
                data[key], expires[key] = value, ttl and now + ttl
                return true
            end,
            delete = function(_, key) data[key], expires[key] = nil, nil end,
            get_keys = function()
                local keys = {}; for key in pairs(data) do keys[#keys + 1] = key end; return keys
            end,
            incr = function(_, key, delta, init)
                data[key] = (data[key] or init) + delta; return data[key]
            end,
        }
        dict.safe_add = function(_, key, value, ttl)
            if dict:get(key) ~= nil then return nil, "exists" end
            return dict:safe_set(key, value, ttl)
        end
        _G.ngx = { now = function() return now end, shared = { neutree_ai_gateway_inflight = dict },
            timer = { at = function(_, callback) timers[#timers + 1] = callback; return true end },
            sleep = function(delay) now = now + delay end }
        _G.kong = { log = { err = function(...) error(table.concat({...}, "")) end } }
        conf = { route_prefix = "/ee", model_routes = {{ model = "chat", targets = {
            { upstream = "A", upstream_model = "a", priority = 0 },
            { upstream = "B", upstream_model = "b", priority = 1 },
        }}} }
        routing.configure({ conf })
    end)

    after_each(function()
        _G.ngx, _G.kong = original_ngx, original_kong
        package.loaded["kong.tools.uuid"] = original_uuid
    end)

    it("switches later requests and lets only one recovery probe through", function()
        local a, selected = start(); assert.are.equal("A", selected)
        complete(a, "failure")
        local b; b, selected = start(); assert.are.equal("B", selected); complete(b, "success")
        now = 15
        local probe; probe, selected = start(); assert.are.equal("A", selected)
        local concurrent; concurrent, selected = start(); assert.are.equal("B", selected)
        complete(concurrent, "success"); complete(probe, "success")
        local recovered; recovered, selected = start(); assert.are.equal("A", selected)
        complete(recovered, "success")
    end)

    it("preserves single-target routing when there is no alternate destination", function()
        conf.model_routes[1].targets[2] = nil
        routing.configure({ conf })
        complete(start(), "failure")
        local next_request, selected = start()
        assert.are.equal("A", selected)
        complete(next_request, "success")
    end)

    it("does not let old ordinary results erase a failure or reopen after recovery", function()
        local first, late_success, late_failure = start(), start(), start()
        complete(first, "failure"); complete(late_success, "success")
        now = 15
        local probe, selected = start(); assert.are.equal("A", selected)
        local other; other, selected = start(); assert.are.equal("B", selected); complete(other, "success")
        complete(probe, "success"); complete(late_failure, "failure")
        local recovered; recovered, selected = start(); assert.are.equal("A", selected); complete(recovered, "success")
    end)

    it("ignores the result of an expired probe after its replacement starts", function()
        complete(start(), "failure"); now = 15
        local old = start(); now = 76
        local replacement, selected = start(); assert.are.equal("A", selected)
        complete(old, "success")
        local other; other, selected = start(); assert.are.equal("B", selected); complete(other, "success")
        complete(replacement, "failure")
        other, selected = start(); assert.are.equal("B", selected); complete(other, "success")
    end)

    it("returns temporary unavailability when all targets cool, without fail-open", function()
        complete(start(), "failure"); complete(start(), "failure")
        local _, selected, reason = start()
        assert.is_nil(selected); assert.are.equal("targets_unavailable", reason)
        now = 15
        local a, a_name = start(); local b, b_name = start()
        assert.are.equal("A", a_name); assert.are.equal("B", b_name)
        _, selected, reason = start()
        assert.is_nil(selected); assert.are.equal("targets_unavailable", reason)
        complete(a, "success"); complete(b, "success")
    end)

    it("releases a probe rejected locally while preserving its recovery requirement", function()
        complete(start(), "failure"); now = 15
        local rejected = start(); routing.finish(rejected)
        local probe, selected = start(); assert.are.equal("A", selected)
        local other; other, selected = start(); assert.are.equal("B", selected)
        complete(other, "success"); complete(probe, "success")
    end)

    it("does not claim a probe on a primary whose inference capacity is full", function()
        conf.model_routes[1].targets[1].max_inflight_requests = 1
        local first = start(); routing.observe(first, "failure"); now = 15
        local other, selected = start(); assert.are.equal("B", selected); complete(other, "success")
        routing.finish(first)
        local probe; probe, selected = start(); assert.are.equal("A", selected)
        routing.observe(probe, "success")
        other, selected = start(); assert.are.equal("B", selected); complete(other, "success")
        routing.finish(probe)
        other, selected = start(); assert.are.equal("A", selected); complete(other, "success")
    end)

    it("does not silently bypass a health storage error or leak its capacity lease", function()
        complete(start(), "failure"); now = 15
        dict.safe_add = function() return nil, "no memory" end
        local request, selected, reason = start()
        assert.is_nil(selected); assert.are.equal("health_unavailable", reason)
        assert.are.equal(0, data[routing.target_key(request, conf.model_routes[1].targets[1])])
    end)

    it("records a contended response after the short state lock is released", function()
        local request = start()
        local lock = "health\0lock\0" .. request.health.key
        dict:safe_add(lock, true, 5)
        complete(request, "failure")
        assert.are.equal(1, #timers)
        dict:delete(lock); timers[1](false)
        local other, selected = start(); assert.are.equal("B", selected); complete(other, "success")
    end)

    it("cleans removed health state without deleting live concurrency leases", function()
        local request = start(); routing.observe(request, "failure")
        local key = request.health.key
        routing.configure({})
        assert.is_nil(dict:get("health\0state\0" .. key))
        assert.are.equal(1, dict:get(key))
        routing.finish(request)
        assert.are.equal(0, dict:get(key))
    end)
end)
