// Command mutation-const marks gremlins survivors on package-level constant
// declarations. See package mutationconst.
package main

import (
	"os"

	"github.com/Broderick-Westrope/anvil/scripts/mutation-const/mutationconst"
)

func main() {
	os.Exit(mutationconst.Run(os.Stdin, os.Stdout, os.Stderr))
}
