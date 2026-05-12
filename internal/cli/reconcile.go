package cli

import (
	"fmt"
	"io"

	"github.com/compozy/skeeper/internal/reconcile"
)

func printDiffSummary(stdout io.Writer, summary reconcile.DiffSummary) {
	for _, namespace := range summary.Namespaces {
		_, _ = fmt.Fprintf(
			stdout,
			"namespace %s: extra=%d missing=%d modified=%d "+
				"sidecar_modified=%d conflicts=%d config_unowned=%d namespace_removed=%d\n",
			namespace.Name,
			namespace.Counts.LocalOnly,
			namespace.Counts.MissingLocal,
			namespace.Counts.LocalModified,
			namespace.Counts.SidecarModified,
			namespace.Counts.BothModifiedConflict,
			namespace.Counts.ConfigUnowned,
			namespace.Counts.NamespaceRemoved,
		)
	}
}
