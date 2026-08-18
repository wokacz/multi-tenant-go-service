// Command openapi writes the API's OpenAPI document to stdout.
//
// The contract is generated from the handlers, but committed to the repository
// so that a change to it is visible as a diff during review. `task openapi`
// regenerates it; `task openapi:check` fails when it is stale.
//
// It lives under tools/ rather than cmd/ because it is a development generator,
// not something the service runs in production.
package main

import (
	"fmt"
	"os"

	"github.com/wokacz/multi-tenant-go-service/internal/api"
)

func main() {
	spec, err := api.Spec()
	if err != nil {
		fmt.Fprintln(os.Stderr, "openapi:", err)
		os.Exit(1)
	}

	if _, err := os.Stdout.Write(spec); err != nil {
		fmt.Fprintln(os.Stderr, "openapi:", err)
		os.Exit(1)
	}
}
