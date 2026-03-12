package featuregate

// DefaultMutableFeatureGate is the global feature gate used by Neutree components.
// Features should be registered in init() functions via DefaultMutableFeatureGate.Add().
var DefaultMutableFeatureGate MutableFeatureGate = NewFeatureGate()
