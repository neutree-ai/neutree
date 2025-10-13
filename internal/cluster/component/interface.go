package component

type Component interface {
	Reconcile() error
	Delete() error
}
