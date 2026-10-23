package bench

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParsePositiveIntegerList(t *testing.T) {
	tests := []struct {
		name    string
		values  []string
		want    []int
		wantErr string
	}{
		{
			name:   "comma separated and repeated values",
			values: []string{"4,1", "2"},
			want:   []int{1, 2, 4},
		},
		{
			name:    "zero is invalid",
			values:  []string{"0"},
			wantErr: "must be > 0",
		},
		{
			name:    "non integer is invalid",
			values:  []string{"two"},
			wantErr: "invalid",
		},
		{
			name:    "empty item is invalid",
			values:  []string{"1,"},
			wantErr: "must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePositiveIntegerList(tt.values, "values")
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestOptionsValidate(t *testing.T) {
	valid := Options{
		Workspace:     "default",
		Endpoint:      "chat",
		InputLens:     []int{16384},
		OutputLens:    []int{2048},
		Concurrencies: []int{2},
		NumPrompts:    20,
		RequestRate:   "inf",
		OutputDir:     "results",
		Timeout:       30 * time.Minute,
	}

	tests := []struct {
		name    string
		mutate  func(*Options)
		wantErr string
	}{
		{
			name: "valid finite rate",
			mutate: func(o *Options) {
				o.RequestRate = "2.5"
			},
		},
		{
			name: "missing endpoint",
			mutate: func(o *Options) {
				o.Endpoint = ""
			},
			wantErr: "endpoint",
		},
		{
			name: "zero prompts",
			mutate: func(o *Options) {
				o.NumPrompts = 0
			},
			wantErr: "num-prompts",
		},
		{
			name: "non positive rate",
			mutate: func(o *Options) {
				o.RequestRate = "0"
			},
			wantErr: "request-rate",
		},
		{
			name: "missing output directory",
			mutate: func(o *Options) {
				o.OutputDir = ""
			},
			wantErr: "output-dir",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := valid
			tt.mutate(&options)
			err := options.Validate()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestExpandCasesUsesStableCartesianProduct(t *testing.T) {
	cases := expandCases([]int{8, 4}, []int{32, 16}, []int{2, 1})

	require.Equal(t, []Case{
		{InputLen: 4, OutputLen: 16, Concurrency: 1},
		{InputLen: 4, OutputLen: 16, Concurrency: 2},
		{InputLen: 4, OutputLen: 32, Concurrency: 1},
		{InputLen: 4, OutputLen: 32, Concurrency: 2},
		{InputLen: 8, OutputLen: 16, Concurrency: 1},
		{InputLen: 8, OutputLen: 16, Concurrency: 2},
		{InputLen: 8, OutputLen: 32, Concurrency: 1},
		{InputLen: 8, OutputLen: 32, Concurrency: 2},
	}, cases)
	require.Equal(t, "input-4-output-16-concurrency-1", cases[0].Name())
}

func TestParsePositiveIntegerListExported(t *testing.T) {
	values, err := ParsePositiveIntegerList([]string{"3,1"}, "concurrencies")
	require.NoError(t, err)
	require.Equal(t, []int{1, 3}, values)
}
