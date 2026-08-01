package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// choice is one selectable line in a picker.
type choice struct {
	Label string
	Hint  string
	Value string
}

// interactive reports whether we can draw a picker: both ends must be a terminal,
// or the command is being scripted and should use flags instead.
func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// errCancelled is returned when the user backs out of a picker.
var errCancelled = fmt.Errorf("cancelled")

// pick draws an arrow-key list and returns the chosen index.
//
// Rendering goes to stderr so a picked-then-piped invoice still has clean stdout.
func pick(prompt string, choices []choice) (int, error) {
	if len(choices) == 0 {
		return 0, fmt.Errorf("nothing to choose from")
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return 0, err
	}
	defer term.Restore(fd, oldState)

	cursor := 0
	drawn := 0

	draw := func() {
		if drawn > 0 {
			// Walk back over what we drew last time and clear it.
			fmt.Fprintf(os.Stderr, "\x1b[%dA", drawn)
		}
		fmt.Fprintf(os.Stderr, "\r\x1b[J")

		fmt.Fprintf(os.Stderr, "\x1b[1m?\x1b[0m %s \x1b[2m(↑↓ to move, enter to select, esc to cancel)\x1b[0m\r\n", prompt)
		for i, item := range choices {
			marker := "  "
			label := item.Label
			if i == cursor {
				marker = "\x1b[36m❯\x1b[0m "
				label = "\x1b[36m" + label + "\x1b[0m"
			}
			line := marker + label
			if item.Hint != "" {
				line += "  \x1b[2m" + item.Hint + "\x1b[0m"
			}
			fmt.Fprint(os.Stderr, line+"\r\n")
		}
		drawn = len(choices) + 1
	}

	draw()
	buffer := make([]byte, 3)

	for {
		read, err := os.Stdin.Read(buffer)
		if err != nil {
			return 0, err
		}

		switch {
		case read == 1 && (buffer[0] == '\r' || buffer[0] == '\n'):
			fmt.Fprint(os.Stderr, "\r\x1b[J")
			return cursor, nil
		case read == 1 && (buffer[0] == 3 || buffer[0] == 'q'): // ctrl-c
			fmt.Fprint(os.Stderr, "\r\x1b[J")
			return 0, errCancelled
		case read == 1 && buffer[0] == 27: // bare escape
			fmt.Fprint(os.Stderr, "\r\x1b[J")
			return 0, errCancelled
		case read == 1 && (buffer[0] == 'k'):
			cursor = (cursor - 1 + len(choices)) % len(choices)
		case read == 1 && (buffer[0] == 'j'):
			cursor = (cursor + 1) % len(choices)
		case read == 3 && buffer[0] == 27 && buffer[1] == '[':
			switch buffer[2] {
			case 'A':
				cursor = (cursor - 1 + len(choices)) % len(choices)
			case 'B':
				cursor = (cursor + 1) % len(choices)
			}
		}
		draw()
	}
}

// prompt asks for a line of text, showing a default the user can accept with enter.
func prompt(question, fallback string) (string, error) {
	if fallback != "" {
		fmt.Fprintf(os.Stderr, "\x1b[1m?\x1b[0m %s \x1b[2m(%s)\x1b[0m ", question, fallback)
	} else {
		fmt.Fprintf(os.Stderr, "\x1b[1m?\x1b[0m %s ", question)
	}

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return fallback, nil
	}
	return line, nil
}
