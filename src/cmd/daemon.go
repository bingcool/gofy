package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var DaemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "daemon run",
	Long:  "daemon run",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("daemon run")
	},
}
