// Package drones owns physical-drone creation and lookup rules.
package drones

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
	"github.com/sunnyside/atlas/atlas-backend/internal/validation"
)

var ErrForbidden = errors.New("drone operation is forbidden")

type Service struct {
	txManager repositories.TxManager
	now       func() time.Time
}

type CreateInput struct {
	Name                string
	FlightControllerUID string
	SerialNumber        string
	VehicleType         models.VehicleType
}

func NewService(txManager repositories.TxManager, now func() time.Time) (*Service, error) {
	if txManager == nil {
		return nil, errors.New("drone transaction manager is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{txManager: txManager, now: now}, nil
}

func (s *Service) Create(ctx context.Context, principal models.Principal, input CreateInput) (models.Drone, error) {
	if principal.User.Role != models.RoleAdmin || principal.User.Status != "active" ||
		principal.Organization.Status != "active" || principal.User.OrganizationID != principal.Organization.ID {
		return models.Drone{}, ErrForbidden
	}
	input.Name = strings.TrimSpace(input.Name)
	input.FlightControllerUID = strings.TrimSpace(input.FlightControllerUID)
	input.SerialNumber = strings.TrimSpace(input.SerialNumber)
	if input.Name == "" {
		return models.Drone{}, &validation.Error{Field: "name", Code: validation.CodeRequired, Message: "drone name is required"}
	}
	if len(input.Name) > 120 {
		return models.Drone{}, &validation.Error{Field: "name", Code: validation.CodeTooLong, Message: "drone name must not exceed 120 characters"}
	}
	if input.VehicleType == "" {
		input.VehicleType = models.VehicleTypeUnknown
	}
	if !validVehicleType(input.VehicleType) {
		return models.Drone{}, &validation.Error{Field: "vehicleType", Code: validation.CodeInvalidFormat, Message: "vehicle type is not supported"}
	}

	var drone models.Drone
	err := s.txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		var err error
		drone, err = repos.Drones().Create(ctx, models.NewDrone{
			OrganizationID:      principal.Organization.ID,
			Name:                input.Name,
			FlightControllerUID: input.FlightControllerUID,
			SerialNumber:        input.SerialNumber,
			VehicleType:         input.VehicleType,
			Now:                 s.now().UTC(),
		})
		return err
	})
	return drone, err
}

func validVehicleType(vehicleType models.VehicleType) bool {
	switch vehicleType {
	case models.VehicleTypeUnknown, models.VehicleTypeMulticopter,
		models.VehicleTypeVTOL:
		return true
	default:
		return false
	}
}

func (s *Service) Find(ctx context.Context, organizationID, droneID string) (models.Drone, error) {
	var drone models.Drone
	err := s.txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		var err error
		drone, err = repos.Drones().FindByID(ctx, organizationID, droneID)
		return err
	})
	return drone, err
}
