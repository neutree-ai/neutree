package options

import (
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestClusterOptions_DefaultPortRange(t *testing.T) {
	o := NewClusterOptions()

	if got := o.PortRange(); got != v1.DefaultPortRange {
		t.Fatalf("default port range: got %+v want %+v", got, v1.DefaultPortRange)
	}
}

func TestClusterOptions_ValidatePortRange(t *testing.T) {
	cases := []struct {
		name string
		pr   v1.PortRangeSpec
		want string
	}{
		{name: "start below user range", pr: v1.PortRangeSpec{Start: 1023, End: 21000}, want: "Start >= 1024"},
		{name: "start greater than end", pr: v1.PortRangeSpec{Start: 21000, End: 20000}, want: "Start <= End"},
		{name: "end above node port range", pr: v1.PortRangeSpec{Start: 20000, End: 32768}, want: "End <= 32767"},
		{name: "too small", pr: v1.PortRangeSpec{Start: 20000, End: 20999}, want: "End - Start >= 1000"},
		{name: "valid minimum", pr: v1.PortRangeSpec{Start: 20000, End: 21000}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := NewClusterOptions()
			o.PortRangeStart = tc.pr.Start
			o.PortRangeEnd = tc.pr.End

			err := o.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate error: got %v want substring %q", err, tc.want)
			}
		})
	}
}
