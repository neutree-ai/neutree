package staticnode

// RegistryAuth carries Docker registry credentials for one reconcile pass.
// It is intentionally not part of StaticNode spec/status; the controller
// resolves it from the parent Cluster's ImageRegistry at runtime.
type RegistryAuth struct {
	Server   string
	Username string
	Password string
}

func (a *RegistryAuth) configured() bool {
	return a != nil && a.Server != "" && a.Username != "" && a.Password != ""
}
