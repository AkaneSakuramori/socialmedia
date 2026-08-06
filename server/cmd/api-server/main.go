// Command api-server is the HTTP API entry point of the InChat backend. It is
// intentionally thin: load config, wire dependencies, start — nothing else
// (ENGINEERING.md §2.1).
//
// Optional flags:
//
//	-healthcheck   verify dependency connectivity and exit 0/1 (container
//	               healthcheck usage, DEVOPS.md §5); does not start the server.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/AkaneSakuramori/socialmedia/server/internal/app"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "run dependency health checks and exit")
	flag.Parse()

	if *healthcheck {
		if err := app.Healthcheck(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := app.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "api-server: %v\n", err)
		os.Exit(1)
	}
}
