package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mercereau/hydrolix-metrics-go/internal/build"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version info",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("version: %s\ncommit: %s\ndate: %s\n", build.Version, build.Commit, build.Date)
		},
	})
}
