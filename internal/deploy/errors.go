package deploy

import "errors"

var (
	// ErrUnsupportedDeploymentType is returned when the deployment type is not supported
	ErrUnsupportedDeploymentType = errors.New("unsupported deployment type")

	// ErrInvalidConfiguration is returned when the deployer configuration is invalid
	ErrInvalidConfiguration = errors.New("invalid deployer configuration")
)
