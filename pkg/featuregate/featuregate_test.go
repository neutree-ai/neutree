package featuregate

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAlphaFeature      Feature = "TestAlpha"
	testBetaFeature       Feature = "TestBeta"
	testGAFeature         Feature = "TestGA"
	testDeprecatedFeature Feature = "TestDeprecated"
)

func testFeatures() map[Feature]FeatureSpec {
	return map[Feature]FeatureSpec{
		testAlphaFeature:      {Default: false, PreRelease: Alpha},
		testBetaFeature:       {Default: true, PreRelease: Beta},
		testGAFeature:         {Default: true, PreRelease: GA},
		testDeprecatedFeature: {Default: false, PreRelease: Deprecated},
	}
}

func TestDefaultsByPreRelease(t *testing.T) {
	fg := NewFeatureGate()
	require.NoError(t, fg.Add(testFeatures()))

	assert.False(t, fg.Enabled(testAlphaFeature), "alpha feature should be disabled by default")
	assert.True(t, fg.Enabled(testBetaFeature), "beta feature should be enabled by default")
	assert.True(t, fg.Enabled(testGAFeature), "GA feature should be enabled by default")
	assert.False(t, fg.Enabled(testDeprecatedFeature), "deprecated feature should be disabled by default")
}

func TestSetFromMapOverrides(t *testing.T) {
	fg := NewFeatureGate()
	require.NoError(t, fg.Add(testFeatures()))

	require.NoError(t, fg.SetFromMap(map[string]bool{
		"TestAlpha": true,
		"TestBeta":  false,
	}))

	assert.True(t, fg.Enabled(testAlphaFeature), "alpha feature should be enabled after override")
	assert.False(t, fg.Enabled(testBetaFeature), "beta feature should be disabled after override")
}

func TestSetFromMapRejectsUnknown(t *testing.T) {
	fg := NewFeatureGate()
	require.NoError(t, fg.Add(testFeatures()))

	err := fg.SetFromMap(map[string]bool{
		"UnknownFeature": true,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized feature gate")
}

func TestSetFromMapRejectsDisablingGA(t *testing.T) {
	fg := NewFeatureGate()
	require.NoError(t, fg.Add(testFeatures()))

	err := fg.SetFromMap(map[string]bool{
		"TestGA": false,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be disabled")
}

func TestAddDuplicateSameSpec(t *testing.T) {
	fg := NewFeatureGate()
	require.NoError(t, fg.Add(testFeatures()))

	// Adding the same features with the same spec should succeed.
	err := fg.Add(map[Feature]FeatureSpec{
		testAlphaFeature: {Default: false, PreRelease: Alpha},
	})
	assert.NoError(t, err)
}

func TestAddDuplicateDifferentSpec(t *testing.T) {
	fg := NewFeatureGate()
	require.NoError(t, fg.Add(testFeatures()))

	// Adding a feature with a different spec should fail.
	err := fg.Add(map[Feature]FeatureSpec{
		testAlphaFeature: {Default: true, PreRelease: Beta},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestEnabledUnknownFeature(t *testing.T) {
	fg := NewFeatureGate()
	assert.False(t, fg.Enabled("NonExistent"), "unknown feature should return false")
}

func TestKnownFeatures(t *testing.T) {
	fg := NewFeatureGate()
	require.NoError(t, fg.Add(testFeatures()))

	known := fg.KnownFeatures()
	assert.Len(t, known, 4)
	// KnownFeatures returns sorted results.
	assert.Contains(t, known[0], "TestAlpha")
	assert.Contains(t, known[1], "TestBeta")
	assert.Contains(t, known[2], "TestDeprecated")
	assert.Contains(t, known[3], "TestGA")
}

func TestAddFlagParsing(t *testing.T) {
	fg := NewFeatureGate()
	require.NoError(t, fg.Add(testFeatures()))

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fg.AddFlag(fs)

	err := fs.Parse([]string{"--feature-gates", "TestAlpha=true,TestBeta=false"})
	require.NoError(t, err)

	assert.True(t, fg.Enabled(testAlphaFeature))
	assert.False(t, fg.Enabled(testBetaFeature))
}

func TestAddFlagParsingInvalidFormat(t *testing.T) {
	fg := NewFeatureGate()
	require.NoError(t, fg.Add(testFeatures()))

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fg.AddFlag(fs)

	err := fs.Parse([]string{"--feature-gates", "TestAlpha"})
	assert.Error(t, err)
}

func TestAddFlagParsingInvalidValue(t *testing.T) {
	fg := NewFeatureGate()
	require.NoError(t, fg.Add(testFeatures()))

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fg.AddFlag(fs)

	err := fs.Parse([]string{"--feature-gates", "TestAlpha=notabool"})
	assert.Error(t, err)
}

func TestParseFeatureGateValue(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]bool
		wantErr bool
	}{
		{
			name:  "single feature",
			input: "Foo=true",
			want:  map[string]bool{"Foo": true},
		},
		{
			name:  "multiple features",
			input: "Foo=true,Bar=false",
			want:  map[string]bool{"Foo": true, "Bar": false},
		},
		{
			name:  "empty string",
			input: "",
			want:  map[string]bool{},
		},
		{
			name:    "missing value",
			input:   "Foo",
			wantErr: true,
		},
		{
			name:    "invalid bool",
			input:   "Foo=maybe",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFeatureGateValue(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
