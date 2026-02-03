package v1beta1

import "github.com/neutree-ai/neutree/pkg/scheme"

var (
	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &scheme.Builder{}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func init() { //nolint:gochecknoinits
	SchemeBuilder.Register(
		&ExternalEndpoint{},
		&ExternalEndpointList{},
	)

	SchemeBuilder.RegisterTable(
		map[string]string{
			"external_endpoints": "ExternalEndpoint",
		},
	)
}
