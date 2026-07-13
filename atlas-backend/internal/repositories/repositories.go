package repositories

import "context"

// Repositories is the transaction-scoped collection of Atlas repositories. Add
// one accessor here for each new repository; every returned repository must be
// constructed from the transaction supplied by TxManager.
type Repositories interface {
	Auth() AuthRepository
	Drones() DroneRepository
	VehicleAgents() VehicleAgentRepository
	VehicleAgentBindings() VehicleAgentBindingRepository
	CommunicationLinks() CommunicationLinkRepository
	EnrollmentTokens() EnrollmentTokenRepository
}

// TxManager owns transaction mechanics and supplies the transaction-scoped
// repository collection to services.
type TxManager interface {
	WithinTransaction(context.Context, func(Repositories) error) error
}
