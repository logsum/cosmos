package main

import (
	"context"
	"cosmos/app"
	"fmt"
	"os"
)

func main() {
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
