package console

import (
	"github.com/spf13/cobra"
)

type CommandConsole interface {
	Handle(cmd *cobra.Command)
}
