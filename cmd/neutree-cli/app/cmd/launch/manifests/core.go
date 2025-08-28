package manifests

import (
	"embed"
)

//go:embed db.tar
var NeutreeCoreDBInitScriptsTar embed.FS

func OverWriteDBInitScripts(config embed.FS) {
	NeutreeCoreDBInitScriptsTar = config
}

//go:embed neutree-core.tar
var NeutreeDeployManifestsTar embed.FS

func OverWriteDeployManifests(config embed.FS) {
	NeutreeDeployManifestsTar = config
}
