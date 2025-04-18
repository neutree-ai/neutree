package manifests

import (
	"embed"
)

//go:embed db.tar
var NeutreeCoreDBInitScriptsTar embed.FS

//go:embed neutree-core.tar
var NeutreeDeployManifestsTar embed.FS
