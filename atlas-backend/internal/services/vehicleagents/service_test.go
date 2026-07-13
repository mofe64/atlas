package vehicleagents

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
)

type fakeTxManager struct{ repos repositories.Repositories }

func (m fakeTxManager) WithinTransaction(_ context.Context, work func(repositories.Repositories) error) error {
	return work(m.repos)
}

type fakeRepositories struct {
	drones   *fakeDroneRepository
	agents   *fakeAgentRepository
	bindings *fakeBindingRepository
	tokens   *fakeEnrollmentTokenRepository
	links    *fakeCommunicationLinkRepository
}

func (fakeRepositories) Auth() repositories.AuthRepository                    { return nil }
func (r fakeRepositories) Drones() repositories.DroneRepository               { return r.drones }
func (r fakeRepositories) VehicleAgents() repositories.VehicleAgentRepository { return r.agents }
func (r fakeRepositories) VehicleAgentBindings() repositories.VehicleAgentBindingRepository {
	return r.bindings
}
func (r fakeRepositories) CommunicationLinks() repositories.CommunicationLinkRepository {
	return r.links
}
func (r fakeRepositories) EnrollmentTokens() repositories.EnrollmentTokenRepository { return r.tokens }

type fakeDroneRepository struct {
	drones      map[string]models.Drone
	byUID       map[string]string
	createCount int
}

func (r *fakeDroneRepository) Create(_ context.Context, input models.NewDrone) (models.Drone, error) {
	r.createCount++
	drone := models.Drone{
		ID: "drone-1", OrganizationID: input.OrganizationID, Name: input.Name,
		FlightControllerUID: input.FlightControllerUID, SerialNumber: input.SerialNumber,
		VehicleType: input.VehicleType, Status: models.DroneStatusActive,
		CreatedAt: input.Now, UpdatedAt: input.Now,
	}
	r.drones[drone.ID] = drone
	if drone.FlightControllerUID != "" {
		r.byUID[drone.FlightControllerUID] = drone.ID
	}
	return drone, nil
}

func (r *fakeDroneRepository) FindByID(_ context.Context, organizationID, droneID string) (models.Drone, error) {
	drone, ok := r.drones[droneID]
	if !ok || drone.OrganizationID != organizationID {
		return models.Drone{}, repositories.ErrNotFound
	}
	return drone, nil
}

func (r *fakeDroneRepository) FindByFlightControllerUID(_ context.Context, organizationID, uid string) (models.Drone, error) {
	droneID, ok := r.byUID[uid]
	if !ok {
		return models.Drone{}, repositories.ErrNotFound
	}
	return r.FindByID(context.Background(), organizationID, droneID)
}

func (r *fakeDroneRepository) AssignIdentityIfUnset(_ context.Context, organizationID, droneID string, identity models.DroneIdentity) (models.Drone, error) {
	drone, err := r.FindByID(context.Background(), organizationID, droneID)
	if err != nil {
		return models.Drone{}, err
	}
	if drone.FlightControllerUID != "" && drone.FlightControllerUID != identity.FlightControllerUID {
		return models.Drone{}, repositories.ErrConflict
	}
	drone.FlightControllerUID = identity.FlightControllerUID
	drone.SerialNumber = identity.SerialNumber
	drone.VehicleType = identity.VehicleType
	drone.UpdatedAt = identity.Now
	r.drones[drone.ID] = drone
	r.byUID[identity.FlightControllerUID] = drone.ID
	return drone, nil
}

type fakeAgentRepository struct {
	agents      map[string]models.VehicleAgent
	byInstall   map[string]string
	createCount int
}

func (r *fakeAgentRepository) Create(_ context.Context, input models.NewVehicleAgent) (models.VehicleAgent, error) {
	r.createCount++
	agent := models.VehicleAgent{
		ID: "agent-1", OrganizationID: input.OrganizationID, InstallationID: input.InstallationID,
		PublicKey: input.PublicKey, Status: models.VehicleAgentStatusActive,
		AgentVersion: input.AgentVersion, ProtocolVersion: input.ProtocolVersion,
		DeviceProfile: input.DeviceProfile, Capabilities: input.Capabilities,
		EnrollmentRequestID: input.EnrollmentRequestID, EnrolledAt: input.Now, UpdatedAt: input.Now,
	}
	r.agents[agent.ID] = agent
	r.byInstall[agent.InstallationID] = agent.ID
	return agent, nil
}

func (r *fakeAgentRepository) FindByID(_ context.Context, organizationID, agentID string) (models.VehicleAgent, error) {
	agent, ok := r.agents[agentID]
	if !ok || agent.OrganizationID != organizationID {
		return models.VehicleAgent{}, repositories.ErrNotFound
	}
	return agent, nil
}

func (r *fakeAgentRepository) FindByInstallationID(_ context.Context, organizationID, installationID string) (models.VehicleAgent, error) {
	agentID, ok := r.byInstall[installationID]
	if !ok {
		return models.VehicleAgent{}, repositories.ErrNotFound
	}
	return r.FindByID(context.Background(), organizationID, agentID)
}

type fakeBindingRepository struct {
	bindings    map[string]models.VehicleAgentBinding
	createCount int
}

func (r *fakeBindingRepository) Create(_ context.Context, input models.NewVehicleAgentBinding) (models.VehicleAgentBinding, error) {
	r.createCount++
	binding := models.VehicleAgentBinding{
		ID: "binding-1", OrganizationID: input.OrganizationID, VehicleAgentID: input.VehicleAgentID,
		DroneID: input.DroneID, Status: input.Status, Attachment: input.Attachment, BoundAt: input.Now,
	}
	r.bindings[binding.ID] = binding
	return binding, nil
}

func (r *fakeBindingRepository) FindByID(_ context.Context, organizationID, bindingID string) (models.VehicleAgentBinding, error) {
	binding, ok := r.bindings[bindingID]
	if !ok || binding.OrganizationID != organizationID {
		return models.VehicleAgentBinding{}, repositories.ErrNotFound
	}
	return binding, nil
}

func (r *fakeBindingRepository) FindActiveByAgent(_ context.Context, organizationID, agentID string) (models.VehicleAgentBinding, error) {
	for _, binding := range r.bindings {
		if binding.OrganizationID == organizationID && binding.VehicleAgentID == agentID && binding.Status == models.VehicleAgentBindingActive {
			return binding, nil
		}
	}
	return models.VehicleAgentBinding{}, repositories.ErrNotFound
}

func (r *fakeBindingRepository) FindActiveByDrone(_ context.Context, organizationID, droneID string) (models.VehicleAgentBinding, error) {
	for _, binding := range r.bindings {
		if binding.OrganizationID == organizationID && binding.DroneID == droneID && binding.Status == models.VehicleAgentBindingActive {
			return binding, nil
		}
	}
	return models.VehicleAgentBinding{}, repositories.ErrNotFound
}

func (r *fakeBindingRepository) End(_ context.Context, organizationID, bindingID, reason string, now time.Time) (models.VehicleAgentBinding, error) {
	binding, err := r.FindByID(context.Background(), organizationID, bindingID)
	if err != nil {
		return models.VehicleAgentBinding{}, err
	}
	binding.Status = models.VehicleAgentBindingEnded
	binding.EndedAt = &now
	binding.EndReason = reason
	r.bindings[bindingID] = binding
	return binding, nil
}

type fakeEnrollmentTokenRepository struct {
	token models.EnrollmentToken
}

type fakeCommunicationLinkRepository struct {
	closedBindingID string
	closeReason     string
}

func (*fakeCommunicationLinkRepository) Create(context.Context, models.NewCommunicationLink) (models.CommunicationLink, error) {
	panic("unexpected Create call")
}
func (*fakeCommunicationLinkRepository) FindByID(context.Context, string, string) (models.CommunicationLink, error) {
	panic("unexpected FindByID call")
}
func (*fakeCommunicationLinkRepository) FindBySessionInstanceID(context.Context, string, string, string) (models.CommunicationLink, error) {
	panic("unexpected FindBySessionInstanceID call")
}
func (*fakeCommunicationLinkRepository) RecordHeartbeat(context.Context, string, string, models.CommunicationLinkHealth, time.Time) (models.CommunicationLink, error) {
	panic("unexpected RecordHeartbeat call")
}
func (*fakeCommunicationLinkRepository) Close(context.Context, string, string, string, time.Time) (models.CommunicationLink, error) {
	panic("unexpected Close call")
}
func (r *fakeCommunicationLinkRepository) CloseOpenForBinding(_ context.Context, _ string, bindingID, reason string, _ time.Time) error {
	r.closedBindingID = bindingID
	r.closeReason = reason
	return nil
}
func (*fakeCommunicationLinkRepository) FindCurrentCommandLink(context.Context, string, string) (models.CommunicationLink, error) {
	panic("unexpected FindCurrentCommandLink call")
}

func (r *fakeEnrollmentTokenRepository) Create(_ context.Context, input models.NewEnrollmentToken) (models.EnrollmentToken, error) {
	r.token = models.EnrollmentToken{
		ID: "token-1", OrganizationID: input.OrganizationID, CreatedByUserID: input.CreatedByUserID,
		TokenHash: append([]byte(nil), input.TokenHash...), ScopedDroneID: input.ScopedDroneID,
		CreatedAt: input.Now, ExpiresAt: input.ExpiresAt,
	}
	return r.token, nil
}

func (r *fakeEnrollmentTokenRepository) FindByHashForUpdate(_ context.Context, hash []byte) (models.EnrollmentToken, error) {
	if !bytes.Equal(hash, r.token.TokenHash) {
		return models.EnrollmentToken{}, repositories.ErrNotFound
	}
	return r.token, nil
}

func (r *fakeEnrollmentTokenRepository) MarkUsed(_ context.Context, tokenID, requestID, agentID, bindingID string, now time.Time) error {
	if r.token.ID != tokenID || r.token.UsedAt != nil {
		return repositories.ErrConflict
	}
	r.token.UsedAt = &now
	r.token.EnrollmentRequestID = requestID
	r.token.EnrolledVehicleAgentID = agentID
	r.token.EnrolledBindingID = bindingID
	return nil
}

func newFixture(t *testing.T, now *time.Time) (*Service, fakeRepositories) {
	t.Helper()
	repos := fakeRepositories{
		drones:   &fakeDroneRepository{drones: map[string]models.Drone{}, byUID: map[string]string{}},
		agents:   &fakeAgentRepository{agents: map[string]models.VehicleAgent{}, byInstall: map[string]string{}},
		bindings: &fakeBindingRepository{bindings: map[string]models.VehicleAgentBinding{}},
		tokens:   &fakeEnrollmentTokenRepository{},
		links:    &fakeCommunicationLinkRepository{},
	}
	service, err := NewService(fakeTxManager{repos: repos}, ServiceConfig{Now: func() time.Time { return *now }})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, repos
}

func adminPrincipal() models.Principal {
	return models.Principal{
		User:         models.User{ID: "user-1", OrganizationID: "org-1", Role: models.RoleAdmin, Status: "active"},
		Organization: models.Organization{ID: "org-1", Status: "active"},
	}
}

func TestEnrollmentCreatesAgentDroneAndBindingAndReplays(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	service, repos := newFixture(t, &now)
	token, err := service.CreateEnrollmentToken(context.Background(), adminPrincipal(), CreateEnrollmentTokenInput{})
	if err != nil {
		t.Fatalf("CreateEnrollmentToken() error = %v", err)
	}
	if token.Token == "" || token.EnrollmentTokenID == "" || repos.tokens.token.TokenHash == nil {
		t.Fatal("enrollment token result or stored digest is missing")
	}
	if bytes.Equal([]byte(token.Token), repos.tokens.token.TokenHash) {
		t.Fatal("repository stored the raw enrollment token instead of its digest")
	}

	input := EnrollInput{
		Token: token.Token, RequestID: "request-1", InstallationID: "install-1",
		PublicKey: make([]byte, 32), AgentVersion: "0.1.0", ProtocolVersion: "1",
		DroneName: "Atlas One", VehicleType: models.VehicleTypeMulticopter,
		Attachment: models.FlightControllerAttachment{Transport: "serial", ObservedUID: "fc-123", BaudRate: 921600},
	}
	first, err := service.Enroll(context.Background(), input)
	if err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}
	if first.Agent.OrganizationID != "org-1" || first.Drone.OrganizationID != "org-1" || first.Binding.OrganizationID != "org-1" {
		t.Fatalf("enrollment escaped token organization: %#v", first)
	}
	if first.Binding.Status != models.VehicleAgentBindingActive || first.Binding.Attachment.Transport != "serial" {
		t.Fatalf("binding = %#v", first.Binding)
	}

	replayed, err := service.Enroll(context.Background(), input)
	if err != nil {
		t.Fatalf("idempotent Enroll() error = %v", err)
	}
	if replayed.Agent.ID != first.Agent.ID || replayed.Binding.ID != first.Binding.ID || replayed.Drone.ID != first.Drone.ID {
		t.Fatalf("replayed enrollment changed result: first=%#v replay=%#v", first, replayed)
	}
	if repos.agents.createCount != 1 || repos.drones.createCount != 1 || repos.bindings.createCount != 1 {
		t.Fatalf("idempotent enrollment recreated rows: agents=%d drones=%d bindings=%d", repos.agents.createCount, repos.drones.createCount, repos.bindings.createCount)
	}
}

func TestEnrollmentTokenCannotBeReusedForDifferentRequest(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	service, _ := newFixture(t, &now)
	token, err := service.CreateEnrollmentToken(context.Background(), adminPrincipal(), CreateEnrollmentTokenInput{})
	if err != nil {
		t.Fatal(err)
	}
	input := EnrollInput{
		Token: token.Token, RequestID: "request-1", InstallationID: "install-1",
		PublicKey: make([]byte, 32), AgentVersion: "0.1.0", ProtocolVersion: "1",
		DroneName: "Atlas One", Attachment: models.FlightControllerAttachment{ObservedUID: "fc-123"},
	}
	if _, err := service.Enroll(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	input.RequestID = "request-2"
	if _, err := service.Enroll(context.Background(), input); !errors.Is(err, ErrEnrollmentTokenUsed) {
		t.Fatalf("Enroll() error = %v, want ErrEnrollmentTokenUsed", err)
	}
}

func TestEnrollmentReplayMustMatchOriginalInstallationIdentity(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	service, _ := newFixture(t, &now)
	token, err := service.CreateEnrollmentToken(context.Background(), adminPrincipal(), CreateEnrollmentTokenInput{})
	if err != nil {
		t.Fatal(err)
	}
	input := EnrollInput{
		Token: token.Token, RequestID: "request-1", InstallationID: "install-1",
		PublicKey: make([]byte, 32), AgentVersion: "0.1.0", ProtocolVersion: "1",
		DroneName: "Atlas One", Attachment: models.FlightControllerAttachment{ObservedUID: "fc-123"},
	}
	if _, err := service.Enroll(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	input.InstallationID = "different-installation"
	if _, err := service.Enroll(context.Background(), input); !errors.Is(err, ErrEnrollmentTokenUsed) {
		t.Fatalf("Enroll() error = %v, want ErrEnrollmentTokenUsed", err)
	}
}

func TestEnrollmentRejectsExpiredToken(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	service, _ := newFixture(t, &now)
	token, err := service.CreateEnrollmentToken(context.Background(), adminPrincipal(), CreateEnrollmentTokenInput{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	_, err = service.Enroll(context.Background(), EnrollInput{
		Token: token.Token, RequestID: "request-1", InstallationID: "install-1",
		PublicKey: make([]byte, 32), AgentVersion: "0.1.0", ProtocolVersion: "1",
		Attachment: models.FlightControllerAttachment{ObservedUID: "fc-123"},
	})
	if !errors.Is(err, ErrEnrollmentTokenExpired) {
		t.Fatalf("Enroll() error = %v, want ErrEnrollmentTokenExpired", err)
	}
}

func TestEndBindingClosesItsOpenCommunicationLinks(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	service, repos := newFixture(t, &now)
	repos.bindings.bindings["binding-1"] = models.VehicleAgentBinding{
		ID: "binding-1", OrganizationID: "org-1", VehicleAgentID: "agent-1",
		DroneID: "drone-1", Status: models.VehicleAgentBindingActive,
	}

	binding, err := service.EndBinding(context.Background(), adminPrincipal(), "binding-1", "computer replaced")
	if err != nil {
		t.Fatalf("EndBinding() error = %v", err)
	}
	if binding.Status != models.VehicleAgentBindingEnded {
		t.Fatalf("binding status = %q, want ended", binding.Status)
	}
	if repos.links.closedBindingID != binding.ID || repos.links.closeReason != "binding ended: computer replaced" {
		t.Fatalf("link close = binding %q reason %q", repos.links.closedBindingID, repos.links.closeReason)
	}
}
