package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Set via ldflags at build time
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "ghayma",
	Short: "Espace-Tech Cloud CLI",
	Long:  "Deploy and manage your applications on Espace-Tech Cloud",
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the CLI version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("ghayma %s\n", version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
