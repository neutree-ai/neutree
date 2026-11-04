local spec_dir = (debug.getinfo(1, "S").source:sub(2)):match("(.*/)") or "./"
local routing = assert(loadfile(spec_dir .. "../routing.lua"))()

describe("passive target failover", function()
    local now, data, expires, dict, conf, original_ngx, original_kong, original_uuid, serial, timers, original_collector, cooldowns

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
        original_collector = package.loaded["kong.plugins.neutree-metrics.collectors.model_routing"]
        cooldowns = {}
        package.loaded["kong.plugins.neutree-metrics.collectors.model_routing"] = {
            cooldown = function(key) cooldowns[key] = (cooldowns[key] or 0) + 1 end,
        }
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
        _G.kong = { node = { get_id = function() return "node-1" end }, log = { err = function(...) error(table.concat({...}, "")) end } }
        conf = { route_prefix = "/ee", model_routes = {{ model = "chat", targets = {
            { upstream = "A", upstream_model = "a", priority = 0 },
            { upstream = "B", upstream_model = "b", priority = 1 },
        }}} }
    end)

    after_each(function()
        package.loaded["kong.plugins.neutree-metrics.collectors.model_routing"] = original_collector
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

    it("counts committed cooldowns, not stale failures, fallback traffic or recovery", function()
        local first, stale = start(), start()
        complete(first, "failure")
        complete(stale, "failure")
        assert.are.equal(1, cooldowns[first.health.key])
        complete(start(), "success")
        assert.are.equal(1, cooldowns[first.health.key])
        now = 15
        complete(start(), "failure")
        assert.are.equal(2, cooldowns[first.health.key])
        now = 30
        complete(start(), "success")
        complete(start(), "success")
        assert.are.equal(2, cooldowns[first.health.key])
    end)

    it("preserves single-target routing when there is no alternate destination", function()
        conf.model_routes[1].targets[2] = nil
        complete(start(), "failure")
        local next_request, selected = start()
        assert.are.equal("A", selected)
        complete(next_request, "success")
    end)

    it("ignores old success during cooldown but accepts late failure after recovery", function()
        local first, late_success, late_failure = start(), start(), start()
        complete(first, "failure"); complete(late_success, "success")
        now = 15
        local probe, selected = start(); assert.are.equal("A", selected)
        local other; other, selected = start(); assert.are.equal("B", selected); complete(other, "success")
        complete(probe, "success")
        assert.is_nil(dict:get("health\0state\0" .. probe.health.key))
        complete(late_failure, "failure")
        local fallback; fallback, selected = start(); assert.are.equal("B", selected); complete(fallback, "success")
        now = 30
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

    it("releases probe admission when inference capacity is full", function()
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

    it("preserves current recovery when an expired probe returns concurrently", function()
        complete(start(), "failure"); now = 15
        local old = start(); now = 76
        local current = start()
        local get = dict.get
        local interrupted = false
        dict.get = function(self, key)
            local value = get(self, key)
            -- Pause the old worker inside its state read while it owns the
            -- lock, and deliver the current probe's success on another worker.
            if not interrupted and key == "health\0state\0" .. old.health.key then
                interrupted = true
                complete(current, "success")
            end
            return value
        end
        complete(old, "success")
        for _, callback in ipairs(timers) do callback(false) end
        local recovered, selected = start()
        assert.are.equal("A", selected); complete(recovered, "success")
    end)

    it("still allows only one recovery probe after a long idle cooldown", function()
        complete(start(), "failure"); now = 136
        local probe, selected = start(); assert.are.equal("A", selected)
        local other; other, selected = start(); assert.are.equal("B", selected)
        complete(other, "success"); complete(probe, "success")
    end)

    it("does not count cooling targets as inflight during fallback selection", function()
        local failed = start(); complete(failed, "failure")
        local incr = dict.incr
        dict.incr = function(self, key, ...)
            assert.are_not.equal(failed.health.key, key)
            return incr(self, key, ...)
        end
        local other, selected = start(); assert.are.equal("B", selected)
        complete(other, "success")
    end)

    it("forgets inactive cooldown state after ten minutes", function()
        local failed = start(); complete(failed, "failure")
        now = 600
        assert.is_nil(dict:get("health\0state\0" .. failed.health.key))
        local first, selected = start(); assert.are.equal("A", selected)
        local second; second, selected = start(); assert.are.equal("A", selected)
        complete(first, "failure"); complete(second, "success")
        local fallback; fallback, selected = start(); assert.are.equal("B", selected)
        complete(fallback, "success")
    end)

    it("reports missing storage when a multi-target request is admitted", function()
        dict = nil
        local _, selected, reason = start()
        assert.is_nil(selected); assert.are.equal("health_unavailable", reason)
    end)
end)
