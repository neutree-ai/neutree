// Package compatibility contains the version policy for optional ZCache
// integrations. The policy is intentionally data-driven and exact-match for
// the first iteration; an absent rule is never treated as compatible.
package compatibility

import (
	_ "embed"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed matrix.yaml
var defaultMatrix []byte

type Result string

const (
	Supported   Result = "supported"
	Unsupported Result = "unsupported"
	Unknown     Result = "unknown"
)

type Rule struct {
	Engine        string `yaml:"engine"`
	EngineVersion string `yaml:"engineVersion"`
	ZCacheVersion string `yaml:"zcacheVersion"`
	Result        Result `yaml:"result"`
	Reason        string `yaml:"reason,omitempty"`
}

type file struct {
	Rules []Rule `yaml:"rules"`
}

type Matrix struct {
	rules []Rule
}

func Default() (Matrix, error) {
	return Load(defaultMatrix)
}

func Load(data []byte) (Matrix, error) {
	var parsed file
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return Matrix{}, fmt.Errorf("parse compatibility matrix: %w", err)
	}
	seen := make(map[string]struct{}, len(parsed.Rules))
	for i, rule := range parsed.Rules {
		rule.Engine = strings.TrimSpace(rule.Engine)
		rule.EngineVersion = strings.TrimSpace(rule.EngineVersion)
		rule.ZCacheVersion = strings.TrimSpace(rule.ZCacheVersion)
		if rule.Engine == "" || rule.EngineVersion == "" || rule.ZCacheVersion == "" {
			return Matrix{}, fmt.Errorf("rule %d has missing engine or version", i)
		}
		if rule.Result != Supported && rule.Result != Unsupported {
			return Matrix{}, fmt.Errorf("rule %d has invalid result %q", i, rule.Result)
		}
		key := ruleKey(rule.Engine, rule.EngineVersion, rule.ZCacheVersion)
		if _, exists := seen[key]; exists {
			return Matrix{}, fmt.Errorf("duplicate compatibility rule %s", key)
		}
		seen[key] = struct{}{}
		parsed.Rules[i] = rule
	}
	return Matrix{rules: parsed.Rules}, nil
}

func (m Matrix) Check(engine, engineVersion, zcacheVersion string) (Result, string) {
	key := ruleKey(engine, engineVersion, zcacheVersion)
	for _, rule := range m.rules {
		if ruleKey(rule.Engine, rule.EngineVersion, rule.ZCacheVersion) == key {
			return rule.Result, rule.Reason
		}
	}
	return Unknown, "组合尚未验证"
}

func ruleKey(engine, engineVersion, zcacheVersion string) string {
	return strings.Join([]string{strings.TrimSpace(engine), strings.TrimSpace(engineVersion), strings.TrimSpace(zcacheVersion)}, "\x00")
}
