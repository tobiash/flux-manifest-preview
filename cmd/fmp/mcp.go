package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/tobiash/flux-manifest-preview/pkg/agent"
	"github.com/tobiash/flux-manifest-preview/pkg/agentmcp"
)

func agentFlags(cmd *cobra.Command, root *string, opts *agent.Options) {
	cmd.Flags().StringVar(root, "root", "", "Workspace root (required)")
	cmd.Flags().BoolVar(&opts.Trusted, "trusted", false, "Enable trusted startup capabilities")
	cmd.Flags().DurationVar(&opts.TTL, "ttl", 0, "Handle TTL (0: service default)")
	cmd.Flags().IntVar(&opts.MaxSnapshots, "max-snapshots", 0, "Maximum retained handles (0: service default)")
	cmd.Flags().Int64Var(&opts.MaxBytes, "max-bytes", 0, "Maximum retained bytes (0: service default)")
	cmd.Flags().DurationVar(&opts.Timeout, "timeout", 0, "Per-operation timeout (0: service default)")
	cmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		var err error
		cmd.InheritedFlags().VisitAll(func(f *pflag.Flag) {
			if f.Changed {
				err = fmt.Errorf("legacy flag --%s is not supported by %s", f.Name, cmd.Name())
			}
		})
		return err
	}
}

func mcpCmd() *cobra.Command {
	var root string
	var opts agent.Options
	cmd := &cobra.Command{Use: "mcp", Short: "Serve manifest tools over MCP stdio", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if root == "" {
				return fmt.Errorf("--root is required")
			}
			s, err := agent.New(root, opts)
			if err != nil {
				return err
			}
			defer func() {
				if err := s.Close(); err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), err)
				}
			}()
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return agentmcp.NewServer(s, version).Run(ctx, agentmcp.StdioTransport())
		}}
	agentFlags(cmd, &root, &opts)
	return cmd
}
