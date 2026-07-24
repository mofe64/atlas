package vehicle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/identity"
)

const indoorExploreHoldTimeout = 3 * time.Second

type IndoorExploreOperation struct {
	OperationID string
	MissionID   string
	DroneID     string
	Action      string
	AltitudeM   float64
	RequestedAt time.Time
	Reason      string
}

type IndoorExploreUpdate struct {
	EventID     string
	OperationID string
	MissionID   string
	State       string
	AltitudeM   float64
	ObservedAt  time.Time
	ErrorCode   string
	Message     string
}

type indoorExploreHoldExecutor interface {
	Execute(context.Context, string, string, string) (CommandResult, error)
}

type activeIndoorExplore struct {
	missionID string
	droneID   string
	altitudeM float64
	state     string
}

// IndoorExploreController owns the Stage 3 Native/Agent mission contract.
// Local navigation is deliberately not commissioned in this slice: Start and
// Abort both establish PX4 Hold and report HOLDING instead of fabricating
// takeoff, exploration, or return progress.
type IndoorExploreController struct {
	hold    indoorExploreHoldExecutor
	updates chan IndoorExploreUpdate

	operationMu sync.Mutex
	mu          sync.Mutex
	active      *activeIndoorExplore
}

func NewIndoorExploreController(hold indoorExploreHoldExecutor) (*IndoorExploreController, error) {
	if hold == nil {
		return nil, errors.New("indoor explore requires a Hold executor")
	}
	return &IndoorExploreController{
		hold:    hold,
		updates: make(chan IndoorExploreUpdate, 16),
	}, nil
}

func (c *IndoorExploreController) Capabilities() []string {
	return []string{
		"indoor_explore:contract:v1",
		"indoor_explore:movement_authority:false",
	}
}

func (c *IndoorExploreController) Updates() <-chan IndoorExploreUpdate {
	return c.updates
}

func (c *IndoorExploreController) Apply(ctx context.Context, operation IndoorExploreOperation) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	switch operation.Action {
	case "start":
		c.start(ctx, operation)
	case "abort_and_return":
		c.abortAndReturn(ctx, operation)
	default:
		c.emit(ctx, operation, "FAILED", "UNSUPPORTED_INDOOR_ACTION", "Agent rejected an unsupported Indoor Explore action")
	}
}

func (c *IndoorExploreController) GroundLinkLost() {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	c.discardPendingUpdates()

	c.mu.Lock()
	active := c.active
	if active != nil {
		active.state = "HOLDING"
	}
	c.mu.Unlock()
	if active == nil {
		return
	}

	operation := IndoorExploreOperation{
		OperationID: identity.NewID(),
		MissionID:   active.missionID,
		DroneID:     active.droneID,
		AltitudeM:   active.altitudeM,
	}
	ctx, cancel := context.WithTimeout(context.Background(), indoorExploreHoldTimeout)
	_, err := c.hold.Execute(ctx, operation.OperationID+"-hold", "hold", "{}")
	cancel()
	if err != nil {
		c.emit(context.Background(), operation, "FAILED", "GROUND_LINK_HOLD_FAILED", fmt.Sprintf("Ground link ended and PX4 Hold failed: %v", err))
		return
	}
	c.emit(context.Background(), operation, "HOLDING", "GROUND_LINK_LOST", "Ground-station session ended; Indoor Explore remains in PX4 Hold")
}

func (c *IndoorExploreController) discardPendingUpdates() {
	for {
		select {
		case <-c.updates:
		default:
			return
		}
	}
}

func (c *IndoorExploreController) start(ctx context.Context, operation IndoorExploreOperation) {
	if err := validateIndoorExploreOperation(operation); err != nil {
		c.emit(ctx, operation, "FAILED", "INVALID_INDOOR_MISSION", err.Error())
		return
	}

	c.mu.Lock()
	if c.active != nil {
		c.mu.Unlock()
		c.emit(ctx, operation, "FAILED", "INDOOR_AUTHORITY_BUSY", "Another Indoor Explore mission already owns the contract")
		return
	}
	c.active = &activeIndoorExplore{
		missionID: operation.MissionID,
		droneID:   operation.DroneID,
		altitudeM: operation.AltitudeM,
		state:     "STARTING",
	}
	c.mu.Unlock()

	c.emit(ctx, operation, "STARTING", "", "Indoor Explore contract accepted; recording the selected mission height")
	if err := c.enterHold(ctx, operation); err != nil {
		c.setState(operation.MissionID, "FAILED")
		c.emit(ctx, operation, "FAILED", "START_HOLD_FAILED", fmt.Sprintf("PX4 Hold failed before local navigation could start: %v", err))
		return
	}
	c.setState(operation.MissionID, "HOLDING")
	c.emit(ctx, operation, "HOLDING", "LOCAL_NAVIGATION_NOT_COMMISSIONED", "Mission contract is active in PX4 Hold; takeoff and local navigation are introduced in later stages")
}

func (c *IndoorExploreController) abortAndReturn(ctx context.Context, operation IndoorExploreOperation) {
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if active == nil || active.missionID != operation.MissionID {
		c.emit(ctx, operation, "FAILED", "INDOOR_MISSION_NOT_ACTIVE", "Agent has no matching active Indoor Explore mission")
		return
	}
	operation.DroneID = active.droneID
	operation.AltitudeM = active.altitudeM
	if err := c.enterHold(ctx, operation); err != nil {
		c.setState(operation.MissionID, "FAILED")
		c.emit(ctx, operation, "FAILED", "ABORT_HOLD_FAILED", fmt.Sprintf("Abort requested but PX4 Hold failed: %v", err))
		return
	}
	c.setState(operation.MissionID, "HOLDING")
	c.emit(ctx, operation, "HOLDING", "RETURN_CONTROLLER_NOT_COMMISSIONED", "Exploration is stopped in PX4 Hold; automatic return-to-start is not available until the return controller is implemented")
}

func (c *IndoorExploreController) enterHold(ctx context.Context, operation IndoorExploreOperation) error {
	holdContext, cancel := context.WithTimeout(ctx, indoorExploreHoldTimeout)
	defer cancel()
	_, err := c.hold.Execute(holdContext, operation.OperationID+"-hold", "hold", "{}")
	return err
}

func (c *IndoorExploreController) setState(missionID, state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil && c.active.missionID == missionID {
		c.active.state = state
	}
}

func (c *IndoorExploreController) emit(ctx context.Context, operation IndoorExploreOperation, state, errorCode, message string) {
	update := IndoorExploreUpdate{
		EventID:     identity.NewID(),
		OperationID: operation.OperationID,
		MissionID:   operation.MissionID,
		State:       state,
		AltitudeM:   operation.AltitudeM,
		ObservedAt:  time.Now().UTC(),
		ErrorCode:   errorCode,
		Message:     message,
	}
	select {
	case c.updates <- update:
	case <-ctx.Done():
	}
}

func validateIndoorExploreOperation(operation IndoorExploreOperation) error {
	if operation.OperationID == "" || operation.MissionID == "" || operation.DroneID == "" {
		return errors.New("Indoor Explore operation, mission, and drone identifiers are required")
	}
	if !supportedIndoorExploreAltitude(operation.AltitudeM) {
		return errors.New("Indoor Explore altitude must be exactly 0.5 m, 1 m, or 2 m")
	}
	return nil
}

func supportedIndoorExploreAltitude(altitudeM float64) bool {
	return altitudeM == 0.5 || altitudeM == 1.0 || altitudeM == 2.0
}
