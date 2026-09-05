package cmd

import (
	"fmt"
	"os"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/mcpauth"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage MCP servers",
}

var mcpAuthCmd = &cobra.Command{
	Use:   "auth <server-name>",
	Short: "Authenticate with an OAuth-enabled MCP server",
	Args:  cobra.ExactArgs(1),
	RunE:  runMCPAuth,
}

func init() {
	mcpAuthCmd.Flags().BoolP("force", "f", false, "Force re-authentication even if already authenticated")
	mcpCmd.AddCommand(mcpAuthCmd)
}

func runMCPAuth(cmd *cobra.Command, args []string) error {
	serverName := args[0]
	force, _ := cmd.Flags().GetBool("force")
	ctx := cmd.Context()

	// Load config.
	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return err
	}
	dataDir, _ := cmd.Flags().GetString("data-dir")
	debug, _ := cmd.Flags().GetBool("debug")

	store, err := config.Init(cwd, dataDir, debug)
	if err != nil {
		return err
	}

	cfg := store.Config()

	// Look up the MCP server in config.
	mcpCfg, ok := cfg.MCP[serverName]
	if !ok {
		return fmt.Errorf("MCP server %q not found in config", serverName)
	}

	// Open the project DB.
	if err := os.MkdirAll(cfg.Options.ProjectDirectory, 0o700); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}
	conn, err := db.ConnectGlobal(ctx)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.ReleaseGlobal() //nolint:errcheck

	queries := db.New(conn)

	res, err := mcpauth.Authorize(ctx, mcpauth.Options{
		ServerName: serverName,
		Config:     mcpCfg,
		Resolver:   store.Resolver(),
		Queries:    queries,
		Force:      force,
		OpenURL:    browser.OpenURL,
		Progress: func(stage mcpauth.Stage, detail string) {
			switch stage {
			case mcpauth.StageRegistering:
				fmt.Println("Registering client with authorization server...")
			case mcpauth.StageAwaitingBrowser:
				fmt.Println("Opening browser to authenticate...")
				fmt.Println()
				fmt.Println("If the browser does not open, visit:")
				fmt.Println(detail)
				fmt.Println()
				fmt.Println("Waiting for authentication callback...")
			case mcpauth.StageExchanging:
				fmt.Println("Exchanging authorization code for token...")
			}
		},
	})
	if err != nil {
		return err
	}
	if res.AlreadyValid {
		fmt.Println("Already authenticated with MCP server", serverName)
		fmt.Println("Use --force to re-authenticate.")
		return nil
	}
	fmt.Println()
	fmt.Println("Successfully authenticated with MCP server", serverName)
	return nil
}
