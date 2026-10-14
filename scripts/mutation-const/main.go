// Command mutation-const marks gremlins survivors that Go coverage can't
// reach: package-level constants and switch case conditions. See package
// mutationconst.
package main

import (
	"os"

	"github.com/Broderick-Westrope/anvil/scripts/mutation-const/mutationconst"
)

func main() {
	os.Exit(mutationconst.Run(os.Stdin, os.Stdout, os.Stderr))
}
