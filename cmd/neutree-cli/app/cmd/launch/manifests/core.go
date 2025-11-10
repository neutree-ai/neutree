package manifests

import (
	"embed"
)

//go:embed neutree-core.tar
var NeutreeDeployManifestsTar embed.FS
