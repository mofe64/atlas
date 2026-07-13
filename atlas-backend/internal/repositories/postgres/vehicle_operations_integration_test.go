package postgres

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/database"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
)

func TestVehicleOperationsRepositoryLifecycle(t *testing.T) {
	databaseURL := os.Getenv("ATLAS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ATLAS_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to test PostgreSQL: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping test PostgreSQL: %v", err)
	}

	txManager := database.NewPostgresTxManager(pool, NewRepositories)
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := now.UnixNano()
	sessionHash := randomBytes(t, 32)
	var organization models.Organization
	var user models.User
	err = txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		var err error
		organization, user, err = repos.Auth().CreateRegistration(
			ctx,
			models.NewOrganization{Name: "Vehicle Operations Integration", Slug: fmt.Sprintf("vehicle-operations-%d", suffix)},
			models.NewUser{
				Email:           fmt.Sprintf("vehicle-operations-%d@example.com", suffix),
				NormalizedEmail: fmt.Sprintf("vehicle-operations-%d@example.com", suffix),
				DisplayName:     "Vehicle Operations Admin", PasswordHash: "test-only-hash", Role: models.RoleAdmin,
			},
			models.NewSession{TokenHash: sessionHash, DeviceName: "integration test", Now: now, ExpiresAt: now.Add(time.Hour)},
		)
		return err
	})
	if err != nil {
		t.Fatalf("create integration organization: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM communication_links WHERE organization_id = $1", organization.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM vehicle_agent_enrollment_tokens WHERE organization_id = $1", organization.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM vehicle_agent_bindings WHERE organization_id = $1", organization.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM vehicle_agents WHERE organization_id = $1", organization.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM drones WHERE organization_id = $1", organization.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1", user.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM organizations WHERE id = $1", organization.ID)
	})

	var drone models.Drone
	var agent models.VehicleAgent
	var binding models.VehicleAgentBinding
	var token models.EnrollmentToken
	var link models.CommunicationLink
	err = txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		var err error
		drone, err = repos.Drones().Create(ctx, models.NewDrone{
			OrganizationID: organization.ID, Name: "Integration Drone",
			FlightControllerUID: fmt.Sprintf("fc-%d", suffix), VehicleType: models.VehicleTypeMulticopter, Now: now,
		})
		if err != nil {
			return err
		}
		agent, err = repos.VehicleAgents().Create(ctx, models.NewVehicleAgent{
			OrganizationID: organization.ID, InstallationID: fmt.Sprintf("install-%d", suffix),
			PublicKey: randomBytes(t, 32), AgentVersion: "0.1.0", ProtocolVersion: "1",
			DeviceProfile: models.DeviceProfile{Hostname: "integration-pi", TotalMemoryBytes: 8 * 1024 * 1024 * 1024},
			Capabilities:  []string{"command", "telemetry"}, EnrollmentRequestID: fmt.Sprintf("request-%d", suffix), Now: now,
		})
		if err != nil {
			return err
		}
		binding, err = repos.VehicleAgentBindings().Create(ctx, models.NewVehicleAgentBinding{
			OrganizationID: organization.ID, VehicleAgentID: agent.ID, DroneID: drone.ID,
			Status:     models.VehicleAgentBindingActive,
			Attachment: models.FlightControllerAttachment{Transport: "serial", BaudRate: 921600, SystemID: 1, ComponentID: 1, ObservedUID: drone.FlightControllerUID},
			Now:        now,
		})
		if err != nil {
			return err
		}
		token, err = repos.EnrollmentTokens().Create(ctx, models.NewEnrollmentToken{
			OrganizationID: organization.ID, CreatedByUserID: user.ID, TokenHash: randomBytes(t, 32),
			ScopedDroneID: drone.ID, Now: now, ExpiresAt: now.Add(time.Hour),
		})
		if err != nil {
			return err
		}
		if err := repos.EnrollmentTokens().MarkUsed(ctx, token.ID, agent.EnrollmentRequestID, agent.ID, binding.ID, now); err != nil {
			return err
		}
		link, err = repos.CommunicationLinks().Create(ctx, models.NewCommunicationLink{
			OrganizationID: organization.ID, VehicleAgentBindingID: binding.ID,
			SessionInstanceID: fmt.Sprintf("session-%d", suffix), Transport: "grpc",
			Roles: []string{"command", "telemetry"}, CommandEligible: true, Now: now,
		})
		return err
	})
	if err != nil {
		t.Fatalf("create vehicle operations graph: %v", err)
	}
	if agent.DeviceProfile.Hostname != "integration-pi" || len(agent.Capabilities) != 2 {
		t.Fatalf("agent JSON fields did not round-trip: %#v", agent)
	}
	if binding.Attachment.BaudRate != 921600 || link.Status != models.CommunicationLinkConnecting {
		t.Fatalf("binding/link did not round-trip: binding=%#v link=%#v", binding, link)
	}

	err = txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		_, err := repos.VehicleAgentBindings().Create(ctx, models.NewVehicleAgentBinding{
			OrganizationID: organization.ID, VehicleAgentID: agent.ID, DroneID: drone.ID,
			Status: models.VehicleAgentBindingPending, Now: now.Add(time.Second),
		})
		return err
	})
	if !errors.Is(err, repositories.ErrConflict) {
		t.Fatalf("second current binding error = %v, want repositories.ErrConflict", err)
	}

	latency := 18.25
	packetLoss := 0.01
	err = txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		var err error
		link, err = repos.CommunicationLinks().RecordHeartbeat(ctx, organization.ID, link.ID, models.CommunicationLinkHealth{
			LatencyMS: &latency, PacketLossEstimate: &packetLoss,
		}, now.Add(5*time.Second))
		if err != nil {
			return err
		}
		current, err := repos.CommunicationLinks().FindCurrentCommandLink(ctx, organization.ID, binding.ID)
		if err != nil {
			return err
		}
		if current.ID != link.ID {
			return fmt.Errorf("current command link = %s, want %s", current.ID, link.ID)
		}
		link, err = repos.CommunicationLinks().Close(ctx, organization.ID, link.ID, "integration complete", now.Add(6*time.Second))
		return err
	})
	if err != nil {
		t.Fatalf("heartbeat and close communication link: %v", err)
	}
	if link.Status != models.CommunicationLinkClosed || link.FirstHeartbeatAt == nil || link.LatencyMS == nil || *link.LatencyMS != latency {
		t.Fatalf("final communication link = %#v", link)
	}
}

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()
	value := make([]byte, size)
	if _, err := cryptorand.Read(value); err != nil {
		t.Fatalf("generate random test bytes: %v", err)
	}
	return value
}
