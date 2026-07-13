// atlas-sitl-fixture prepares PX4 SITL for command integration tests.
// It is deliberately separate from the production Atlas Agent command surface.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	actionpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/action"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	address := os.Getenv("ATLAS_MAVSDK_GRPC_ADDR")
	if address == "" {
		address = "127.0.0.1:50051"
	}
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fatal(err)
	}
	defer connection.Close()
	client := actionpb.NewActionServiceClient(connection)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := retryAction(ctx, "arm", func() (*actionpb.ActionResult, error) {
		response, err := client.Arm(ctx, &actionpb.ArmRequest{})
		return response.GetActionResult(), err
	}); err != nil {
		fatal(err)
	}
	if err := retryAction(ctx, "takeoff", func() (*actionpb.ActionResult, error) {
		response, err := client.Takeoff(ctx, &actionpb.TakeoffRequest{})
		return response.GetActionResult(), err
	}); err != nil {
		fatal(err)
	}
	time.Sleep(6 * time.Second)
}

func retryAction(ctx context.Context, label string, action func() (*actionpb.ActionResult, error)) error {
	for {
		result, err := action()
		if err == nil && result.GetResult() == actionpb.ActionResult_RESULT_SUCCESS {
			fmt.Printf("%s succeeded\n", label)
			return nil
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
			return fmt.Errorf("%s: %s", label, result.GetResultStr())
		case <-time.After(time.Second):
		}
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
