package onboardsetup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type CommandResult struct {
	Output string
	Err    error
}

type Runner interface {
	Run(ctx context.Context, name string, args ...string) CommandResult
	LookPath(name string) (string, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) CommandResult {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	return CommandResult{Output: strings.TrimSpace(string(output)), Err: err}
}

func (ExecRunner) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

type ApplyRunner struct {
	Runner Runner
	DryRun bool
	Output interface{ Write([]byte) (int, error) }
}

func (runner ApplyRunner) Run(ctx context.Context, name string, args ...string) error {
	if runner.DryRun {
		_, _ = fmt.Fprintf(runner.Output, "+ %s %s\n", name, shellDisplay(args))
		return nil
	}
	result := runner.Runner.Run(ctx, name, args...)
	if result.Err != nil {
		return fmt.Errorf("%s %s: %w%s", name, shellDisplay(args), result.Err, outputSuffix(result.Output))
	}
	return nil
}

func (runner ApplyRunner) RunOptional(ctx context.Context, name string, args ...string) {
	if runner.DryRun {
		_, _ = fmt.Fprintf(runner.Output, "+ %s %s || true\n", name, shellDisplay(args))
		return
	}
	_ = runner.Runner.Run(ctx, name, args...).Err
}

func shellDisplay(values []string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !strings.ContainsAny(value, " \t\n\"'") {
			parts = append(parts, value)
			continue
		}
		parts = append(parts, fmt.Sprintf("%q", value))
	}
	return strings.Join(parts, " ")
}

func outputSuffix(output string) string {
	if output == "" {
		return ""
	}
	return ": " + output
}

func isRoot() bool {
	return os.Geteuid() == 0
}
