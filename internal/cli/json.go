package cli

import (
	"fmt"
	"io"

	"github.com/compozy/skeeper/internal/sidecar"
)

func printJSONStatus(stdout io.Writer, result any, ok bool, failure string) error {
	if err := sidecar.PrintJSON(stdout, result); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s", failure)
	}
	return nil
}
