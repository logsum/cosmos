package main

import (
	"context"
	"cosmos/app"
	"fmt"
	"os"
)

const version = "0.2.0"

func main() {
	// Handle --version flag
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println(version)
		os.Exit(0)
	}

	ctx := context.Background()

	// Bootstrap application
	application, err := app.Bootstrap(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cosmos: %v\n", err)
		os.Exit(1)
	}

	// Run application (blocks until exit)
	if err := application.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "cosmos: %v\n", err)
		os.Exit(1)
	}
}
