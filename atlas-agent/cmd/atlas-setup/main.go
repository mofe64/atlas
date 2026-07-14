package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/sunnyside/atlas/atlas-agent/internal/onboardsetup"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(arguments []string) int {
	command := "install"
	if len(arguments) > 0 && (arguments[0] == "install" || arguments[0] == "doctor") {
		command, arguments = arguments[0], arguments[1:]
	}
	flags := flag.NewFlagSet("atlas-setup "+command, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	dryRun := flags.Bool("dry-run", false, "show the installation plan without changing the computer")
	nonInteractive := flags.Bool("non-interactive", false, "use discovered/default values without prompting")
	allowUnsupported := flags.Bool("allow-unsupported", false, "allow development validation on a non-target platform")
	installHailo := flags.Bool("install-hailo", false, "deprecated: use sudo atlas-hailo-setup before atlas-setup")
	replaceLegacy := flags.Bool("replace-legacy", false, "stop and archive deprecated Atlas systemd units")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if *installHailo {
		fmt.Fprintln(os.Stderr, "atlas-setup: --install-hailo is deprecated; run sudo atlas-hailo-setup, reboot if requested, then run sudo atlas-setup")
		return 2
	}
	root := os.Getenv("ATLAS_SETUP_ROOT")
	if root == "" {
		root = "/"
	}
	if root != "/" && !*dryRun {
		fmt.Fprintln(os.Stderr, "ATLAS_SETUP_ROOT is restricted to --dry-run validation")
		return 2
	}
	options := onboardsetup.Options{
		DryRun:               *dryRun,
		NonInteractive:       *nonInteractive,
		AllowUnsupported:     *allowUnsupported,
		ReplaceLegacy:        *replaceLegacy,
		Paths:                onboardsetup.DefaultPaths(root),
		Input:                os.Stdin,
		Output:               os.Stdout,
		ArchitectureOverride: runtime.GOARCH,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runner := onboardsetup.ExecRunner{}
	switch command {
	case "install":
		result, err := onboardsetup.Install(ctx, runner, options)
		if err != nil {
			fmt.Fprintf(os.Stderr, "atlas-setup: %v\n", err)
			return 1
		}
		if result.RebootRequired {
			return 3
		}
		return 0
	case "doctor":
		checks, err := onboardsetup.Doctor(ctx, runner, options.Paths, os.Stdout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "atlas-setup doctor: %v\n", err)
			return 1
		}
		if onboardsetup.HasFailures(checks) {
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown atlas-setup command %q\n", command)
		return 2
	}
}
