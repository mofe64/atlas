// Package services contains Atlas application workflows.
//
// Services own transaction boundaries through repository.TxManager. Repositories
// passed into a TxManager callback are transaction-scoped; private helpers should
// accept that repository set when they need to participate in an existing
// workflow transaction.
package services

import "github.com/sunnyside/atlas/atlas-backend/internal/repository"

// Dependencies groups the shared persistence dependencies used to build Atlas services.
type Dependencies struct {
	TxManager    repository.TxManager
	Repositories repository.Repositories
}

// Services collects the application workflows exposed to HTTP and agent-channel adapters.
type Services struct {
	VehicleAgents           *VehicleAgentService
	VehicleAgentConnections *VehicleAgentConnectionService
	Telemetry               *TelemetryService
	VehicleActions          *VehicleActionService
	Missions                *MissionService
	Fleet                   *FleetService
}

// New wires Atlas service instances around the same repository and transaction dependencies.
func New(deps Dependencies) Services {
	return Services{
		VehicleAgents:           NewVehicleAgentService(deps.TxManager),
		VehicleAgentConnections: NewVehicleAgentConnectionService(deps.TxManager),
		Telemetry:               NewTelemetryService(deps.TxManager),
		VehicleActions:          NewVehicleActionService(deps.TxManager, deps.Repositories),
		Missions:                NewMissionService(deps.TxManager, deps.Repositories),
		Fleet:                   NewFleetService(deps.Repositories),
	}
}
