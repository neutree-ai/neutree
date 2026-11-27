local dir = debug.getinfo(1, "S").source:sub(2):match("(.*/)")
package.loaded["kong.llm.drivers.shared"] = {}
package.loaded["kong.llm.drivers.openai"] = {}
package.loaded["kong.tools.string"] = {strip = function(s) return s end}
local handler = assert(loadfile(dir .. "../handler.lua"))()

describe("trusted front statistics", function()
    it("skips body reads and billing even when ACL short-circuited access", function()
        local old = kong
        _G.kong = {ctx = {shared = {neutree_internal_unpacked = true}}}
        for _, phase in ipairs({"header_filter", "body_filter", "log", "access"}) do
            handler[phase](handler, {})
        end
        _G.kong = old
    end)
end)
