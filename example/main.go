package main

import (
	"time"

	"github.com/B00TK1D/bottomline"
)

func main() {
	term, err := bottomline.New("> ")
	if err != nil {
		panic(err)
	}
	defer term.Close()

	term.Register("help", func(t *bottomline.Terminal) {
		t.Println("Available commands:")
		t.Println("  help - Show this help")
		t.Println("  clear - Clear screen and fix prompt at bottom")
		t.Println("  quit/exit - Exit the program")
		t.Println("  echo <message> - Echo a message")
		t.Println("  read <path> - Read a file")
		t.Println("  count <number> - Count to a number")
		t.Println("  set color <color> - Set color (red/green/blue)")
		t.Println("  set size <size> - Set size (small/medium/large)")
		t.Println("  set name <name> - Set name")
	})

	term.Register("clear", func(t *bottomline.Terminal) {
		t.Clear()
		t.Println("Terminal cleared - prompt now fixed at bottom")
	})

	term.Register("quit", func(t *bottomline.Terminal) {
		t.Println("Goodbye!")
		t.Exit()
	})

	term.Register("exit", func(t *bottomline.Terminal) {
		t.Println("Goodbye!")
		t.Exit()
	})

	term.Register("base/:message", func(t *bottomline.Terminal, message string) {
		t.SetPrompt(message + " > ")
	})

	term.Register("read/:path:path", func(t *bottomline.Terminal, path string) {
		t.Printf("Reading file: %s\n", path)
	})

	term.Register("count/:number:int", func(t *bottomline.Terminal, number int) {
		t.Printf("Counting to %d\n", number)
	})

	term.Register("set/color/red", func(t *bottomline.Terminal) {
		t.Println("Color set to red")
	})

	term.Register("set/color/green", func(t *bottomline.Terminal) {
		t.Println("Color set to green")
	})

	term.Register("set/color/blue", func(t *bottomline.Terminal) {
		t.Println("Color set to blue")
	})

	term.Register("set/name/:name", func(t *bottomline.Terminal, name string) {
		t.Printf("Name set to: %s\n", name)
	})

	term.Register("sleep/:seconds:int", func(t *bottomline.Terminal, seconds int) {
		t.Printf("Sleeping for %d seconds...\n", seconds)
		time.Sleep(time.Duration(seconds) * time.Second)
		t.Println("Awake!")
	})

	term.Register("confirm", func(t *bottomline.Terminal) {
		c := t.Input("Please confirm (y/n): ")
		if c == "y" || c == "Y" {
			t.Println("Confirmed!")
		}
	})

	term.Register("select", func(t *bottomline.Terminal) {
		options := []string{"Option 1", "Option 2", "Option 3"}
		choice := t.InputSelect("Please select an option:", options)
		t.Printf("You selected: %s\n", choice)
	})

	term.Register("count", func(t *bottomline.Terminal) {
		number := t.InputInt("Enter a number to count to: ")
		for i := 1; i <= number; i++ {
			t.Printf("Count: %d\n", i)
			time.Sleep(1 * time.Second)
		}
	})

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		count := 0
		for range ticker.C {
			count++
			term.Printf("Background message #%d\n", count)
		}
	}()

	for cmd := range term.Commands() {
		term.Printf("Unknown command: %s\n", cmd)
	}
}
