package bench

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Options defines one vLLM benchmark invocation. The CLI converts its raw
// repeatable flags to the integer slices before calling Run.
type Options struct {
	Workspace     string
	Endpoint      string
	InputLens     []int
	OutputLens    []int
	Concurrencies []int
	NumPrompts    int
	RequestRate   string
	OutputDir     string
	Timeout       time.Duration
}

// Case is one independent point in the input/output/concurrency matrix.
type Case struct {
	InputLen    int `json:"input_len"`
	OutputLen   int `json:"output_len"`
	Concurrency int `json:"concurrency"`
}

func (c Case) Name() string {
	return fmt.Sprintf("input-%d-output-%d-concurrency-%d", c.InputLen, c.OutputLen, c.Concurrency)
}

// Validate rejects invalid user input before the command reads credentials or
// executes anything in a workload runtime.
func (o Options) Validate() error {
	if strings.TrimSpace(o.Workspace) == "" {
		return fmt.Errorf("workspace is required")
	}

	if strings.TrimSpace(o.Endpoint) == "" {
		return fmt.Errorf("endpoint is required")
	}

	if err := validatePositiveValues(o.InputLens, "input-lens"); err != nil {
		return err
	}

	if err := validatePositiveValues(o.OutputLens, "output-lens"); err != nil {
		return err
	}

	if err := validatePositiveValues(o.Concurrencies, "concurrencies"); err != nil {
		return err
	}

	if o.NumPrompts <= 0 {
		return fmt.Errorf("num-prompts must be > 0")
	}

	if !validRequestRate(o.RequestRate) {
		return fmt.Errorf("request-rate must be inf or a finite number > 0")
	}

	if strings.TrimSpace(o.OutputDir) == "" {
		return fmt.Errorf("output-dir is required")
	}

	if o.Timeout < 0 {
		return fmt.Errorf("timeout must be >= 0")
	}

	return nil
}

func validatePositiveValues(values []int, name string) error {
	if len(values) == 0 {
		return fmt.Errorf("%s must not be empty", name)
	}

	for _, value := range values {
		if value <= 0 {
			return fmt.Errorf("%s values must be > 0", name)
		}
	}

	return nil
}

func validRequestRate(value string) bool {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "inf") {
		return true
	}

	rate, err := strconv.ParseFloat(value, 64)

	return err == nil && rate > 0 && !math.IsInf(rate, 0) && !math.IsNaN(rate)
}

// parsePositiveIntegerList accepts both repeatable flags and comma-separated
// values. It returns a fresh sorted, de-duplicated slice so callers do not
// retain mutable flag backing storage.
func parsePositiveIntegerList(values []string, name string) ([]int, error) {
	seen := make(map[int]struct{})

	for _, raw := range values {
		for _, item := range strings.Split(raw, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				return nil, fmt.Errorf("%s values must not be empty", name)
			}

			value, err := strconv.Atoi(item)
			if err != nil {
				return nil, fmt.Errorf("invalid %s value %q: %w", name, item, err)
			}

			if value <= 0 {
				return nil, fmt.Errorf("%s values must be > 0", name)
			}

			seen[value] = struct{}{}
		}
	}

	if len(seen) == 0 {
		return nil, fmt.Errorf("%s must not be empty", name)
	}

	result := make([]int, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}

	sort.Ints(result)

	return result, nil
}

// ParsePositiveIntegerList converts one or more comma-separated CLI values to
// a stable positive integer set.
func ParsePositiveIntegerList(values []string, name string) ([]int, error) {
	return parsePositiveIntegerList(values, name)
}

func expandCases(inputLens, outputLens, concurrencies []int) []Case {
	inputs := sortedDistinct(inputLens)
	outputs := sortedDistinct(outputLens)
	concurrencyValues := sortedDistinct(concurrencies)
	cases := make([]Case, 0, len(inputs)*len(outputs)*len(concurrencyValues))

	for _, inputLen := range inputs {
		for _, outputLen := range outputs {
			for _, concurrency := range concurrencyValues {
				cases = append(cases, Case{
					InputLen:    inputLen,
					OutputLen:   outputLen,
					Concurrency: concurrency,
				})
			}
		}
	}

	return cases
}

func sortedDistinct(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	for _, value := range values {
		seen[value] = struct{}{}
	}

	result := make([]int, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}

	sort.Ints(result)

	return result
}
