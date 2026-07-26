// Package consts holds the literal values shared across the controller.
//
// Deliberately not here: ControllerName and Name in packages ingress and
// gateway. Those derive from their own package paths via reflect, so relocating
// them would make them report this package's path instead and silently break
// the contract with the shipped manifests. A constant that describes where it
// lives has to live where it describes.
//
// This package imports nothing from the module, so it can never form a cycle.
package consts

// Component names. Used as probe path segments and as log labels.
const (
	Healthz = "healthz"
	Readyz  = "readyz"
	Metrics = "metrics"
)

// Controller names registered with the manager. Distinct from the package
// names because one package can own several controllers — gateway runs both
// Gateway and GatewayClass.
const (
	ControllerIngress      = "ingress"
	ControllerGateway      = "gateway"
	ControllerGatewayClass = "gatewayclass"
)

// Flag names and their defaults.
const (
	FlagMetricsAddr = "metrics-bind-address"
	FlagProbeAddr   = "health-probe-bind-address"
	FlagLeaderElect = "leader-elect"
	FlagInstall     = "install"

	DefaultMetricsAddr = ":8080"
	DefaultProbeAddr   = ":8081"
)

// Event vocabulary. Reason answers "why", Action answers "what was attempted";
// the events.k8s.io API separates them so events aggregate cleanly.
const (
	EventTypeWarning = "Warning"

	ReasonUnimplemented = "Unimplemented"
	ActionProvision     = "Provision"
)

// DefaultProvider is the provider used when nothing is annotated, and the name
// given to the classes --install creates.
const DefaultProvider = "tunnel.pizza"

// AnnotationProvider overrides the tunnel provider host. Read from the Ingress
// or Gateway first, then from its class, then falling back to DefaultProvider
// — so a cluster can default to one provider and still send individual
// workloads elsewhere.
const AnnotationProvider = "tunnel.pizza/provider"

// GatewayClassDescription is shown by `kubectl get gatewayclass`.
const GatewayClassDescription = "Public reachability for this cluster via a tunnel provider."

// Messages published while provisioning is unimplemented.
const (
	// MsgUnimplemented is the bare statement, for status conditions.
	MsgUnimplemented = "tunnel provisioning is not implemented yet"
	// MsgUnimplementedFmt adds the provider that would have been used. Takes
	// the provider host.
	MsgUnimplementedFmt = MsgUnimplemented + "; would mint a tunnel from https://%s/tunnel"
)
