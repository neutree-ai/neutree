local spec_dir = (debug.getinfo(1, "S").source:sub(2)):match("(.*/)")

describe("metrics collector dispatch", function()
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

    it("records independent request metrics without routing facts or a source", function()
        local hits, requests, registrations = 0, 0, 0
        entries[1] = { name = "cache", collector = {
            init = function(prom) assert.are.equal(registry, prom); registrations = registrations + 1 end,
            log = function(source)
                assert.is_nil(source)
                if kong.ctx.shared.cache_hit then hits = hits + 1 end
            end,
        } }
        entries[2] = { name = "access", collector = { log = function() requests = requests + 1 end } }
        kong.ctx.shared.cache_hit = true
        metrics.init(); metrics.init(); metrics.log(); metrics.log(); metrics.collect()
        assert.are.equal(1, registrations)
        assert.are.equal(1, hits)
        assert.are.equal(1, requests)
        kong.ctx = { shared = {}, plugin = {} }
        metrics.log()
        assert.are.equal(1, hits)
        assert.are.equal(2, requests)
    end)

    it("passes each source to its own collector and exports once after collection", function()
        local value, observed, sequence = 1, {}, {}
        entries[1] = { name = "capacity", source = { read = function() return value end }, collector = {
            collect = function(source) observed[#observed + 1] = source.read(); sequence[#sequence + 1] = "capacity" end,
        } }
        entries[2] = { name = "other", source = { value = 7 }, collector = {
            collect = function(source) assert.are.equal(7, source.value); sequence[#sequence + 1] = "other" end,
        } }
        assert.are.equal(200, metrics.collect())
        value = 2
        assert.are.equal(200, metrics.collect())
        assert.are.same({1, 2}, observed)
        assert.are.same({"capacity", "other", "capacity", "other"}, sequence)
        assert.are.equal(2, exports)
    end)

    it("isolates initialization failures and retries without reinitializing healthy collectors", function()
        local failing, attempts, initialized, logged = true, 0, 0, 0
        entries[1] = { name = "broken", collector = {
            init = function() attempts = attempts + 1; if failing then error("init failed") end end,
            log = function() logged = logged + 1 end,
        } }
        entries[2] = { name = "healthy", collector = {
            init = function() initialized = initialized + 1 end,
            log = function() logged = logged + 1 end,
        } }
        assert.is_false(metrics.init())
        metrics.log()
        assert.are.equal(1, logged)
        failing = false
        kong.ctx.plugin = {}
        metrics.log()
        assert.are.equal(3, logged)
        assert.are.equal(1, initialized)
        assert.are.equal(3, attempts)
    end)

    it("isolates log and scrape failures without suppressing other collectors or export", function()
        local logged, collected = 0, 0
        entries[1] = { name = "broken", collector = {
            log = function() error("log failed") end,
            collect = function() error("snapshot failed") end,
        } }
        entries[2] = { name = "healthy", collector = {
            log = function() logged = logged + 1 end,
            collect = function() collected = collected + 1 end,
        } }
        metrics.log(); metrics.log()
        assert.are.equal(200, metrics.collect())
        assert.are.equal(1, logged)
        assert.are.equal(1, collected)
        assert.are.equal(1, exports)
        assert.are.equal(2, #errors)
    end)

    it("recovers from an unavailable exporter without losing the request prematurely", function()
        local logged = 0
        entries[1] = { name = "requests", collector = { log = function() logged = logged + 1 end } }
        registry = nil
        assert.is_false(metrics.init())
        metrics.log()
        assert.are.equal(503, metrics.collect())
        registry = {}
        metrics.log()
        assert.are.equal(1, logged)
    end)

    it("returns an error when the shared export fails", function()
        package.loaded["kong.plugins.prometheus.exporter"].collect = function() error("export failed") end
        assert.are.equal(503, metrics.collect())
        assert.are.equal(1, #errors)
    end)
end)
