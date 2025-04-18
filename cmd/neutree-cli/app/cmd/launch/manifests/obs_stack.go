package manifests

import (
	"embed"
)

//go:embed obs-stack.tar
var ObsStackDeployManifestsTar embed.FS
