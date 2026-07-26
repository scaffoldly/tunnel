// Package config carries the runtime knobs shared by the controllers.
package config

// Config is resolved from flags once, in main, and passed to each New.
type Config struct {
	// Install has the controller create default IngressClasses and
	// GatewayClasses at startup, so the shipped manifest is a Deployment and
	// its RBAC rather than a list of classes.
	//
	// Turn it off when the classes are managed elsewhere — Helm, Argo, or any
	// other reconciler that would fight over ownership.
	Install bool
}
