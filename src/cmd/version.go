package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var version = "1.0.0"

var VersionCmd = &cobra.Command{
	Use:   "version",
	Short: "show version",
	Long:  "show version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(version)
	},
}
