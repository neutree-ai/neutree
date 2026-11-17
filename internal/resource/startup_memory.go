package resource

import (
	"regexp"
	"strconv"
)

var (
	vllmModelLoadingRegex     = regexp.MustCompile(`Model loading took ([0-9]+(?:\.[0-9]+)?)\s*(?:GiB|GB)`)
	vllmAvailableKVCacheRegex = regexp.MustCompile(`Available KV cache memory:\s*([0-9]+(?:\.[0-9]+)?)\s*(?:GiB|GB)`)
)

func parseVLLMStartupMemoryMiB(logs string) (*float64, *float64) {
	return parseStartupMemoryMiB(vllmModelLoadingRegex, logs, false),
		parseStartupMemoryMiB(vllmAvailableKVCacheRegex, logs, true)
}

// Startup logs provide replica-level representative values: maximum model
// weights and minimum available KV memory among the records read.
func parseStartupMemoryMiB(pattern *regexp.Regexp, logs string, takeMin bool) *float64 {
	var value float64

	for _, match := range pattern.FindAllStringSubmatch(logs, -1) {
		gib, err := strconv.ParseFloat(match[1], 64)
		if err != nil || gib <= 0 {
			continue
		}

		if value == 0 || (takeMin && gib < value) || (!takeMin && gib > value) {
			value = gib
		}
	}

	if value == 0 {
		return nil
	}

	mib := roundFloat64ToTwoDecimals(value * 1024)
	return &mib
}
