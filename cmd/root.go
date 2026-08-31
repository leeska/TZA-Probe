package cmd

import (
	"fmt"
	"os"

	"github.com/komari-monitor/komari/cmd/flags"

	"github.com/spf13/cobra"
)

func GetEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func defaultDatabaseFile() string {
	const current = "./data/tza-probe.db"
	const legacy = "./data/komari.db"
	if _, err := os.Stat(current); err == nil {
		return current
	}
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return current
}

var RootCmd = &cobra.Command{
	Use:   "tza-probe",
	Short: "TZA Probe monitors infrastructure, carrier latency, and return routes",
	Long: `TZA Probe is a self-hosted infrastructure and network path monitoring system.
It combines host metrics with independently selectable carrier latency and return-route probes.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.SetArgs([]string{"server"})
		cmd.Execute()
	},
}

func Execute() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	RootCmd.PersistentFlags().StringVarP(&flags.DatabaseType, "db-type", "t", "sqlite", "Database type (sqlite)")
	RootCmd.PersistentFlags().StringVarP(&flags.DatabaseFile, "database", "d", defaultDatabaseFile(), "SQLite database file path")
}
