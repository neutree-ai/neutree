package adapter

import "strings"

// LookupPeakFP16TFLOPS uses the most specific model match in a plugin-owned table.
// Unknown products have no default. Values preserve the existing FP16 estimates.
func LookupPeakFP16TFLOPS(product string, specifications map[string]float64) *float64 {
	name := strings.ToUpper(product)
	longest := 0
	var peak *float64
	for model, value := range specifications {
		if len(model) > longest && strings.Contains(name, strings.ToUpper(model)) {
			longest = len(model)
			copy := value
			peak = &copy
		}
	}
	return peak
}
