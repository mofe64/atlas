package repositories

import (
	"context"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
)

type DroneRepository interface {
	Create(context.Context, models.NewDrone) (models.Drone, error)
	FindByID(context.Context, string, string) (models.Drone, error)
	FindByFlightControllerUID(context.Context, string, string) (models.Drone, error)
	AssignIdentityIfUnset(context.Context, string, string, models.DroneIdentity) (models.Drone, error)
}

type VehicleAgentRepository interface {
	Create(context.Context, models.NewVehicleAgent) (models.VehicleAgent, error)
	FindByID(context.Context, string, string) (models.VehicleAgent, error)
	FindByInstallationID(context.Context, string, string) (models.VehicleAgent, error)
}

type VehicleAgentBindingRepository interface {
	Create(context.Context, models.NewVehicleAgentBinding) (models.VehicleAgentBinding, error)
	FindByID(context.Context, string, string) (models.VehicleAgentBinding, error)
	FindActiveByAgent(context.Context, string, string) (models.VehicleAgentBinding, error)
	FindActiveByDrone(context.Context, string, string) (models.VehicleAgentBinding, error)
	End(context.Context, string, string, string, time.Time) (models.VehicleAgentBinding, error)
}

type CommunicationLinkRepository interface {
	Create(context.Context, models.NewCommunicationLink) (models.CommunicationLink, error)
	FindByID(context.Context, string, string) (models.CommunicationLink, error)
	FindBySessionInstanceID(context.Context, string, string, string) (models.CommunicationLink, error)
	RecordHeartbeat(context.Context, string, string, models.CommunicationLinkHealth, time.Time) (models.CommunicationLink, error)
	Close(context.Context, string, string, string, time.Time) (models.CommunicationLink, error)
	CloseOpenForBinding(context.Context, string, string, string, time.Time) error
	FindCurrentCommandLink(context.Context, string, string) (models.CommunicationLink, error)
}

type EnrollmentTokenRepository interface {
	Create(context.Context, models.NewEnrollmentToken) (models.EnrollmentToken, error)
	FindByHashForUpdate(context.Context, []byte) (models.EnrollmentToken, error)
	MarkUsed(context.Context, string, string, string, string, time.Time) error
}
