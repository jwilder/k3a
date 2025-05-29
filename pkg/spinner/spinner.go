package spinner

import (
	"fmt"
	"time"
)

// Spinner displays a simple progress spinner in the terminal until the returned stop function is called.
func Spinner(message string) func() {
	done := make(chan struct{})
	go func() {
		symbols := []string{"|", "/", "-", "\\"}
		i := 0
		for {
			select {
			case <-done:
				return
			default:
				fmt.Printf("\r%s %s", message, symbols[i%len(symbols)])
				time.Sleep(200 * time.Millisecond)
				i++
			}
		}
	}()
	return func() {
		done <- struct{}{}
		fmt.Printf("\r") // Clear spinner line
	}
}
