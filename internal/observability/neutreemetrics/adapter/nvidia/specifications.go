package nvidia

// Nominal per-card FP16 peak estimates carried over from the UI defaults.
var peakFP16TFLOPS = map[string]float64{
	"T4": 65, "L4": 121, "A10": 125, "L20": 119.5, "L40S": 362,
	"A100": 312, "A800": 312, "H100": 989, "H800": 989, "H20": 148, "4090": 165,
}
