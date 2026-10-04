package main

import (
	"context"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/installer"
)

func runDeployment(ctx context.Context, e *env, args []string) error {
	subs := []*subcommand{
		{"status", "", "Show the installed container and image state", deploymentAction(installer.DeploymentStatus)},
		{"logs", "", "Show the latest 100 controller log lines", deploymentAction(installer.DeploymentLogs)},
		{"start", "", "Start a stopped deployment", deploymentAction(installer.DeploymentStart)},
		{"stop", "", "Stop containers without removing them", deploymentAction(installer.DeploymentStop)},
		{"restart", "", "Restart the current containers", deploymentAction(installer.DeploymentRestart)},
		{"update", "", "Compatibility alias for zoomies upgrade", runDeploymentUpdate},
		{"down", "", "Stop and remove containers while keeping the database volume", deploymentAction(installer.DeploymentDown)},
	}
	return runGroup(ctx, e, "deployment", "Operate the Docker or Compose deployment recorded by `zoomies init`.", subs, args)
}

func deploymentAction(action installer.DeploymentAction) func(context.Context, *env, []string) error {
	return deploymentActionNamed("deployment "+string(action), action)
}

func deploymentActionNamed(command string, action installer.DeploymentAction) func(context.Context, *env, []string) error {
	return func(ctx context.Context, e *env, args []string) error {
		fs := newFlagSet(e, "zoomies "+command+" [--config-dir path]",
			"Use the saved deployment record, so container names and Compose files are never guessed.")
		configDir := fs.String("config-dir", "", "directory containing deployment.json (default: "+config.ConfigDir()+")")
		fs.example("zoomies "+command, "zoomies "+command+" --config-dir /etc/zoomies")
		if err := fs.parse(args); err != nil {
			return err
		}
		if err := fs.noMoreArgs(); err != nil {
			return err
		}
		return installer.ControlDeployment(ctx, installer.DeploymentControlOptions{
			ConfigDir: *configDir, Action: action, Out: e.out,
		})
	}
}

// runLogs is the short spelling operators reach for during setup and incident
// response. The nested command remains available for backwards compatibility.
func runLogs(ctx context.Context, e *env, args []string) error {
	return deploymentActionNamed("logs", installer.DeploymentLogs)(ctx, e, args)
}

func runDeploymentUpdate(ctx context.Context, e *env, args []string) error {
	return runUpgradeNamed(ctx, e, args, "deployment update")
}
