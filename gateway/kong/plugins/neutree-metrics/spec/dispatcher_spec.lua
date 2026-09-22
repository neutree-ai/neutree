local spec_dir = (debug.getinfo(1, "S").source:sub(2)):match("(.*/)")

describe("metrics data protocol", function()
    local entries, metrics, registry, errors, exports
    before_each(function()
        entries, registry, errors, exports = {}, {}, {}, 0
        package.loaded["kong.plugins.neutree-metrics.collectors"] = entries
        package.loaded["kong.plugins.prometheus.exporter"] = {
            get_prometheus = function() return registry end,
            collect = function() exports = exports + 1; return 200 end,
        }
        _G.kong = {
            ctx = { shared = {}, plugin = {} },
            log = { err = function(...) errors[#errors + 1] = {...} end },
            response = { exit = function(status) return status end },
        }
        metrics = assert(loadfile(spec_dir .. "../metrics.lua"))()
    end)

    it("connects a cache source through the standard protocol without routing data", function()
        local hits, reads, registrations = 0, 0, 0
        entries[1] = { name = "cache", source = {
            request = function() reads = reads + 1; return { hit = kong.ctx.shared.cache_hit } end,
        }, collector = {
            init = function(prom) assert.are.equal(registry, prom); registrations = registrations + 1 end,
            record = function(data) if data.hit then hits = hits + 1 end end,
        } }
        kong.ctx.shared.cache_hit = true
        metrics.init(); metrics.init(); metrics.log(); metrics.log(); metrics.collect()
        assert.are.equal(1, registrations)
        assert.are.equal(1, reads)
        assert.are.equal(1, hits)
        kong.ctx = { shared = {}, plugin = {} }
        metrics.log()
        assert.are.equal(2, reads)
        assert.are.equal(1, hits)
    end)

    it("takes fresh snapshots and exports once after all collectors", function()
        local value, observed = 1, {}
        entries[1] = { name = "capacity", source = { snapshot = function() return { value = value } end }, collector = {
            collect = function(data) observed[#observed + 1] = data.value end,
        } }
        entries[2] = { name = "other", source = { snapshot = function() return { value = 7 } end }, collector = {
            collect = function(data) observed[#observed + 1] = data.value end,
        } }
        assert.are.equal(200, metrics.collect())
        value = 2
        assert.are.equal(200, metrics.collect())
        assert.are.same({1, 7, 2, 7}, observed)
        assert.are.equal(2, exports)
    end)

    it("skips absent requests and clears stale state for absent or failed snapshots", function()
        local mode, gauge, records = "ok", nil, 0
        entries[1] = { name = "capacity", source = {
            request = function() return nil end,
            snapshot = function()
                if mode == "error" then return nil, "unavailable" end
                if mode == "throw" then error("read failed") end
                if mode == "none" then return nil end
                return { value = 3 }
            end,
        }, collector = {
            record = function() records = records + 1 end,
            collect = function(data) gauge = data and data.value end,
        } }
        metrics.log()
        assert.are.equal(0, records)
        for _, failure in ipairs({"none", "error", "throw"}) do
            mode = "ok"; metrics.collect(); assert.are.equal(3, gauge)
            mode = failure; assert.are.equal(200, metrics.collect()); assert.is_nil(gauge)
        end
        assert.are.equal(2, #errors)
    end)

    it("supports optional stages and never reads unused source methods", function()
        entries[1] = { name = "unused", source = { snapshot = function() error("must not read") end }, collector = {} }
        entries[2] = { name = "missing", collector = { record = function() error("no data") end,
            collect = function(data) assert.is_nil(data) end } }
        metrics.init(); metrics.log(); metrics.collect()
        assert.are.equal(0, #errors)
    end)

    it("isolates initialization failures and retries without reinitializing healthy collectors", function()
        local failing, attempts, initialized, logged = true, 0, 0, 0
        local source = { request = function() return {} end }
        entries[1] = { name = "broken", source = source, collector = {
            init = function() attempts = attempts + 1; if failing then error("init failed") end end,
            record = function() logged = logged + 1 end,
        } }
        entries[2] = { name = "healthy", source = source, collector = {
            init = function() initialized = initialized + 1 end,
            record = function() logged = logged + 1 end,
        } }
        assert.is_false(metrics.init()); metrics.log(); assert.are.equal(1, logged)
        failing = false; kong.ctx.plugin = {}; metrics.log()
        assert.are.equal(3, logged)
        assert.are.equal(1, initialized)
        assert.are.equal(3, attempts)
    end)

    it("isolates source and collector errors while preserving other families and export", function()
        local logged, collected = 0, 0
        entries[1] = { name = "bad-source", source = {
            request = function() return nil, "request unavailable" end,
            snapshot = function() error("snapshot failed") end,
        }, collector = { record = function() error("must not record") end, collect = function(data) assert.is_nil(data) end } }
        entries[2] = { name = "bad-collector", source = {
            request = function() return {} end, snapshot = function() return {} end,
        }, collector = { record = function() error("record failed") end, collect = function() error("collect failed") end } }
        entries[3] = { name = "healthy", source = {
            request = function() return {} end, snapshot = function() return {} end,
        }, collector = { record = function() logged = logged + 1 end, collect = function() collected = collected + 1 end } }
        metrics.log(); metrics.log()
        assert.are.equal(200, metrics.collect())
        assert.are.equal(1, logged); assert.are.equal(1, collected)
        assert.are.equal(1, exports); assert.are.equal(4, #errors)
    end)

    it("recovers from an unavailable exporter without losing the request prematurely", function()
        local logged = 0
        entries[1] = { name = "requests", source = { request = function() return {} end },
            collector = { record = function() logged = logged + 1 end } }
        registry = nil
        assert.is_false(metrics.init()); metrics.log(); assert.are.equal(503, metrics.collect())
        registry = {}; metrics.log(); assert.are.equal(1, logged)
    end)

    it("returns an error when the shared export fails", function()
        package.loaded["kong.plugins.prometheus.exporter"].collect = function() error("export failed") end
        assert.are.equal(503, metrics.collect()); assert.are.equal(1, #errors)
    end)
end)
