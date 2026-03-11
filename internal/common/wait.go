package common

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// WaitToDie waits for termination signals and calls the provided stop function before exiting.
func WaitToDie(stop ...func()) {
	// 1. Create a buffered channel to receive OS signals.
	// A buffer size of 1 or more is recommended to avoid missing signals
	// if your program is briefly busy.
	sigs := make(chan os.Signal, 1)

	// 2. Register the channel to receive notifications for specific signals.
	// We typically listen for SIGINT (Ctrl+C) and SIGTERM (system termination signal).
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)

	// 3. Create a channel to signal when the application should exit (optional, but good for structure).
	done := make(chan bool, 1)

	// 4. Launch a goroutine to handle the received signals.
	go func() {
		// This blocks until a signal is received.
		sig := <-sigs
		fmt.Println()
		fmt.Println("Received signal:", sig)

		for _, fn := range stop {
			fn() // Call each provided stop function to clean up resources.
		}

		// Signal the main goroutine to exit.
		done <- true
	}()

	fmt.Println("Application is running. Press Ctrl+C (SIGINT) or send SIGTERM to exit.")

	// 5. Block the main goroutine until a value is received on the 'done' channel.
	<-done
	fmt.Println("Application exiting gracefully.")
}

// waitForKillSignal listens for termination signals and handles them.
// It uses a channel to notify the caller when a signal has been received.
func WaitForKillSignal(stopChan chan<- struct{}) {
	// Create a channel to receive OS signals.
	signalChan := make(chan os.Signal, 1)

	// Notify signalChan of interrupt and termination signals.
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)

	fmt.Println("Signal listener started. Waiting for a kill signal (SIGINT or SIGTERM)...")

	// Block until a signal is received.
	sig := <-signalChan
	fmt.Printf("\nReceived signal: %s. Shutting down gracefully...\n", sig)

	// Notify the main function that a signal was received.
	close(stopChan)
}
