package app_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/cmd/neutree-api/app"
	"github.com/neutree-ai/neutree/pkg/admission"
)

func TestAdmissionConfigurerPublicContract(t *testing.T) {
	var configure func(*app.AdmissionOptions) error = func(options *app.AdmissionOptions) error {
		var _ *admission.Registry = options.Registry
		return nil
	}

	builder := app.NewBuilder().WithAdmissionConfigurer("enterprise.public-contract", configure)
	require.NotNil(t, builder)

	optionsType := reflect.TypeFor[app.AdmissionOptions]()
	require.Equal(t, 1, optionsType.NumField())
	field := optionsType.Field(0)
	require.Equal(t, "Registry", field.Name)
	require.Equal(t, reflect.TypeFor[*admission.Registry](), field.Type)
}
