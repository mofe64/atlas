// Package vehicle executes the deliberately small Atlas contingency command set.
package vehicle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	actionpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/action"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type CommandResult struct {
	Code    string
	Message string
}

type ActionExecutor struct {
	connection  *grpc.ClientConn
	action      actionpb.ActionServiceClient
	payload     *PayloadController
	ownsPayload bool
	mu          sync.Mutex
	receipts    map[string]commandReceipt
}

type commandReceipt struct {
	result       CommandResult
	errorMessage string
}

func NewActionExecutor(address string, sharedPayload ...*PayloadController) (*ActionExecutor, error) {
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("create MAVSDK action client: %w", err)
	}
	var payload *PayloadController
	ownsPayload := false
	if len(sharedPayload) > 0 && sharedPayload[0] != nil {
		payload = sharedPayload[0]
	} else {
		payload, err = NewPayloadController(address, slog.Default())
		if err != nil {
			_ = connection.Close()
			return nil, err
		}
		ownsPayload = true
	}
	return &ActionExecutor{
		connection:  connection,
		action:      actionpb.NewActionServiceClient(connection),
		payload:     payload,
		ownsPayload: ownsPayload,
		receipts:    make(map[string]commandReceipt),
	}, nil
}

func (e *ActionExecutor) Close() error {
	err := e.connection.Close()
	if e.ownsPayload {
		if payloadErr := e.payload.Close(); err == nil {
			err = payloadErr
		}
	}
	return err
}

func (e *ActionExecutor) Execute(ctx context.Context, commandID, commandType, parametersJSON string) (CommandResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if receipt, exists := e.receipts[commandID]; exists {
		if receipt.errorMessage != "" {
			return receipt.result, errors.New(receipt.errorMessage)
		}
		return receipt.result, nil
	}

	var result *actionpb.ActionResult
	var err error
	switch commandType {
	case "hold":
		var response *actionpb.HoldResponse
		response, err = e.action.Hold(ctx, &actionpb.HoldRequest{})
		if response != nil {
			result = response.GetActionResult()
		}
	case "return_to_launch":
		var response *actionpb.ReturnToLaunchResponse
		response, err = e.action.ReturnToLaunch(ctx, &actionpb.ReturnToLaunchRequest{})
		if response != nil {
			result = response.GetActionResult()
		}
	case "land":
		var response *actionpb.LandResponse
		response, err = e.action.Land(ctx, &actionpb.LandRequest{})
		if response != nil {
			result = response.GetActionResult()
		}
	case "payload_control_begin", "payload_control_renew", "payload_control_end",
		"gimbal_set_angles", "gimbal_set_rates", "gimbal_center", "gimbal_set_roi",
		"camera_set_zoom":
		commandResult, executeErr := e.payload.Execute(ctx, commandType, parametersJSON)
		if executeErr != nil {
			e.receipts[commandID] = commandReceipt{result: commandResult, errorMessage: executeErr.Error()}
			return commandResult, executeErr
		}
		e.receipts[commandID] = commandReceipt{result: commandResult}
		return commandResult, nil
	default:
		return CommandResult{}, fmt.Errorf("unsupported vehicle command %q", commandType)
	}
	if err != nil {
		e.receipts[commandID] = commandReceipt{errorMessage: err.Error()}
		return CommandResult{}, err
	}
	if result == nil {
		err = errors.New("MAVSDK action response did not include a result")
		e.receipts[commandID] = commandReceipt{errorMessage: err.Error()}
		return CommandResult{}, err
	}
	commandResult := CommandResult{
		Code:    result.GetResult().String(),
		Message: result.GetResultStr(),
	}
	if commandResult.Message == "" {
		commandResult.Message = commandResult.Code
	}
	if result.GetResult() != actionpb.ActionResult_RESULT_SUCCESS {
		e.receipts[commandID] = commandReceipt{result: commandResult, errorMessage: commandResult.Message}
		return commandResult, errors.New(commandResult.Message)
	}
	e.receipts[commandID] = commandReceipt{result: commandResult}
	return commandResult, nil
}

func (e *ActionExecutor) DiscoverGimbals(ctx context.Context) ([]int32, error) {
	return e.payload.DiscoverGimbals(ctx)
}

func (e *ActionExecutor) DiscoverCameras(ctx context.Context) ([]int32, error) {
	return e.payload.DiscoverCameras(ctx)
}

func (e *ActionExecutor) Capabilities() []string {
	return append([]string{"command:hold", "command:return_to_launch", "command:land"}, e.payload.Capabilities()...)
}
