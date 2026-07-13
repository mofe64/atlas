package postgres

import (
	"github.com/sunnyside/atlas/atlas-backend/internal/database"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
)

// Repositories is constructed once per transaction. As Atlas gains new domain
// repositories, store each one here so all accessors return objects bound to the
// same database.TxExecutor.
type Repositories struct {
	auth                 repositories.AuthRepository
	drones               repositories.DroneRepository
	vehicleAgents        repositories.VehicleAgentRepository
	vehicleAgentBindings repositories.VehicleAgentBindingRepository
	communicationLinks   repositories.CommunicationLinkRepository
	enrollmentTokens     repositories.EnrollmentTokenRepository
}

// compile-time interface assertion.
var _ repositories.Repositories = (*Repositories)(nil)

func NewRepositories(tx database.TxExecutor) repositories.Repositories {
	return &Repositories{
		auth:                 NewAuthRepository(tx),
		drones:               NewDroneRepository(tx),
		vehicleAgents:        NewVehicleAgentRepository(tx),
		vehicleAgentBindings: NewVehicleAgentBindingRepository(tx),
		communicationLinks:   NewCommunicationLinkRepository(tx),
		enrollmentTokens:     NewEnrollmentTokenRepository(tx),
	}
}

func (r *Repositories) Auth() repositories.AuthRepository {
	return r.auth
}

func (r *Repositories) Drones() repositories.DroneRepository {
	return r.drones
}

func (r *Repositories) VehicleAgents() repositories.VehicleAgentRepository {
	return r.vehicleAgents
}

func (r *Repositories) VehicleAgentBindings() repositories.VehicleAgentBindingRepository {
	return r.vehicleAgentBindings
}

func (r *Repositories) CommunicationLinks() repositories.CommunicationLinkRepository {
	return r.communicationLinks
}

func (r *Repositories) EnrollmentTokens() repositories.EnrollmentTokenRepository {
	return r.enrollmentTokens
}
