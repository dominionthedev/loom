package cmd

import (
	"fmt"
	"os"

	"github.com/dominionthedev/loom/internal/model"
	"github.com/spf13/cobra"
)

var modelsCmd = &cobra.Command{
	Use:   "models",
	Short: "List models available on the Ollama server",
	RunE: func(cmd *cobra.Command, args []string) error {
		base := os.Getenv("OLLAMA_HOST")
		if base == "" {
			base = os.Getenv("OLLACLOUD_HOST")
		}

		models, err := model.ListModels(base)
		if err != nil {
			return fmt.Errorf("models: %w", err)
		}

		if len(models) == 0 {
			fmt.Println("no models found")
			return nil
		}

		fmt.Println()
		for _, m := range models {
			fmt.Printf("  %s\n", m.Name)
		}
		fmt.Println()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(modelsCmd)
}
