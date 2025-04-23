package cmd

import (
	"github.com/bingcool/gofy/src/cmd/command"
	"github.com/spf13/cobra"
)

var CronStopCmd = &cobra.Command{
	Use:   command.CronStopCommandName,
	Short: "stop cron of the gofy",
	Long:  "stop cron of the gofy",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
	},
	PreRun: func(cmd *cobra.Command, args []string) {

	},
	Run: func(cmd *cobra.Command, args []string) {
		stopServer()
	},
	PostRun: func(cmd *cobra.Command, args []string) {

	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
	},
}
