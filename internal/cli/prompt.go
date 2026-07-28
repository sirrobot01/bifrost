package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"slices"
	"strings"

	"golang.org/x/term"
)

// promptAttempts bounds how many times one question is re-asked after an
// unusable answer, so a non-interactive stdin cannot spin forever.
const promptAttempts = 3

// prompter asks setup questions on a terminal. It reads through one buffered
// reader so that answers cannot be split across reads, and it falls back to
// plain line reads when stdin is not a terminal, which keeps a piped answer
// script working.
type prompter struct {
	reader *bufio.Reader
	output io.Writer
	input  *os.File
}

func newPrompter(input *os.File, output io.Writer) *prompter {
	return &prompter{reader: bufio.NewReader(input), output: output, input: input}
}

// ask returns the trimmed answer, or fallback when the answer is empty.
func (p *prompter) ask(question, fallback string) (string, error) {
	label := question
	if fallback != "" {
		label = fmt.Sprintf("%s [%s]", question, fallback)
	}
	if _, err := fmt.Fprintf(p.output, "%s: ", label); err != nil {
		return "", err
	}
	line, err := p.readLine()
	if err != nil {
		return "", err
	}
	p.echoAnswer(line, false)
	if line == "" {
		return fallback, nil
	}
	return line, nil
}

// echoAnswer mirrors the answer back when stdin is not a terminal. A terminal
// echoes typed input itself, but a pipe does not, which would otherwise run
// every question and answer together on one unreadable line.
func (p *prompter) echoAnswer(answer string, hide bool) {
	if term.IsTerminal(int(p.input.Fd())) {
		return
	}
	if hide {
		answer = ""
	}
	_, _ = fmt.Fprintln(p.output, answer)
}

// required re-asks until the answer is non-empty.
func (p *prompter) required(question, fallback string) (string, error) {
	for range promptAttempts {
		answer, err := p.ask(question, fallback)
		if err != nil {
			return "", err
		}
		if answer != "" {
			return answer, nil
		}
		p.reject("this question has no default and must be answered")
	}
	return "", fmt.Errorf("no answer for %q", question)
}

// choice re-asks until the answer is one of options.
func (p *prompter) choice(question string, options []string, fallback string) (string, error) {
	label := fmt.Sprintf("%s (%s)", question, strings.Join(options, "/"))
	for range promptAttempts {
		answer, err := p.ask(label, fallback)
		if err != nil {
			return "", err
		}
		answer = strings.ToLower(answer)
		if slices.Contains(options, answer) {
			return answer, nil
		}
		p.reject("answer must be one of " + strings.Join(options, ", "))
	}
	return "", fmt.Errorf("no valid answer for %q", question)
}

// addrPort re-asks until the answer parses as an IP address and port.
func (p *prompter) addrPort(question, fallback string) (netip.AddrPort, error) {
	for range promptAttempts {
		answer, err := p.ask(question, fallback)
		if err != nil {
			return netip.AddrPort{}, err
		}
		parsed, parseErr := netip.ParseAddrPort(answer)
		if parseErr == nil && parsed.Port() != 0 {
			return parsed, nil
		}
		p.reject("answer must be an IP address and port, for example 127.0.0.1:8096")
	}
	return netip.AddrPort{}, fmt.Errorf("no valid answer for %q", question)
}

// confirm asks a yes or no question.
func (p *prompter) confirm(question string, fallback bool) (bool, error) {
	options := "y/N"
	if fallback {
		options = "Y/n"
	}
	for range promptAttempts {
		answer, err := p.ask(fmt.Sprintf("%s (%s)", question, options), "")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "":
			return fallback, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		p.reject("answer yes or no")
	}
	return false, fmt.Errorf("no valid answer for %q", question)
}

// secret reads an answer without echoing it, so that API tokens do not survive
// in terminal scrollback. Echo suppression needs the raw terminal, so a piped
// stdin falls back to a plain line read.
func (p *prompter) secret(question string) (string, error) {
	if _, err := fmt.Fprintf(p.output, "%s (input hidden): ", question); err != nil {
		return "", err
	}
	// Buffered bytes were already consumed from the terminal, so reading the
	// descriptor directly here would skip them and read the wrong line.
	if p.reader.Buffered() > 0 || !term.IsTerminal(int(p.input.Fd())) {
		answer, err := p.readLine()
		if err != nil {
			return "", err
		}
		p.echoAnswer(answer, true)
		return answer, nil
	}
	raw, err := term.ReadPassword(int(p.input.Fd()))
	if _, printErr := fmt.Fprintln(p.output); printErr != nil {
		return "", printErr
	}
	if err != nil {
		return "", fmt.Errorf("read hidden input: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func (p *prompter) readLine() (string, error) {
	line, err := p.reader.ReadString('\n')
	if err != nil {
		// A final answer without a trailing newline is still an answer; only an
		// empty read at EOF means the input really ran out.
		if errors.Is(err, io.EOF) && strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line), nil
		}
		if errors.Is(err, io.EOF) {
			return "", errors.New("input ended before every question was answered")
		}
		return "", fmt.Errorf("read answer: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func (p *prompter) reject(reason string) {
	_, _ = fmt.Fprintf(p.output, "  %s\n", reason)
}

func (p *prompter) say(format string, arguments ...any) error {
	_, err := fmt.Fprintf(p.output, format+"\n", arguments...)
	return err
}
