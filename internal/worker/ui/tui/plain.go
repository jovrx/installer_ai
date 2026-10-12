package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"remoteops/internal/i18n"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
	"remoteops/internal/worker"
	"remoteops/internal/worker/client"
)

type lineResult struct {
	data []byte
	err  error
}

// Read only the current line, without buffering the following password paste.
// The terminal is restored by runPlain even when an interrupt ends this read.
func readLine(ctx context.Context, input *os.File) <-chan lineResult {
	ch := make(chan lineResult)
	go func() {
		var data []byte
		var one [1]byte
		var err error
		for len(data) <= 8192 {
			var n int
			n, err = input.Read(one[:])
			if err != nil || n == 0 {
				break
			}
			if one[0] == '\n' {
				break
			}
			data = append(data, one[0])
		}
		if len(data) > 8192 {
			err = errInputLong
		}
		select {
		case ch <- lineResult{data, err}:
		case <-ctx.Done():
			clear(data)
		}
	}()
	return ch
}
func awaitLine(ctx context.Context, input *os.File) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-readLine(ctx, input):
		defer clear(r.data)
		return strings.TrimSpace(string(r.data)), r.err
	}
}
func readKey(ctx context.Context, input *os.File) ([]byte, error) {
	ch := make(chan lineResult)
	go func() {
		data, err := term.ReadPassword(int(input.Fd()))
		select {
		case ch <- lineResult{data, err}:
		case <-ctx.Done():
			clear(data)
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.data, r.err
	}
}

func runPlain(ctx context.Context, app *client.App, input *os.File, out io.Writer) error {
	if !term.IsTerminal(int(input.Fd())) {
		err := errors.New(i18n.Text(app.Language(), "worker.plain.terminal"))
		fmt.Fprintln(out, err.Error())
		return err
	}
	state, err := term.GetState(int(input.Fd()))
	if err != nil {
		return errors.New(i18n.Text(app.Language(), "worker.plain.terminal_state"))
	}
	defer term.Restore(int(input.Fd()), state)
	return runPlainSession(ctx, app, out, func() (string, error) { return awaitLine(ctx, input) }, func() ([]byte, error) { return readKey(ctx, input) }, func() <-chan lineResult { return readLine(ctx, input) })
}

func runPlainSession(ctx context.Context, app *client.App, out io.Writer, prompt func() (string, error), password func() ([]byte, error), commandLine func() <-chan lineResult) error {
	s := app.Snapshot()
	fmt.Fprintln(out, i18n.Text(s.Language, "worker.plain.header"))
	for s.State != "ready" {
		for _, message := range messages(s) {
			fmt.Fprintln(out, message)
		}
		fmt.Fprint(out, i18n.Text(s.Language, "worker.plain.path"))
		path, err := prompt()
		if err != nil {
			return plainReadError(ctx, err, s.Language)
		}
		if handled, updated := plainLanguageCommand(ctx, app, out, path); handled {
			if ctx.Err() != nil {
				return nil
			}
			if updated != nil {
				s = *updated
			}
			continue
		}
		if err = app.LoadPairing(strings.Trim(path, "\"")); err != nil {
			fmt.Fprintln(out, client.ErrorText(err, s.Language))
		}
		s = app.Snapshot()
	}
	for {
		printPlainAuthorization(out, s)
		fmt.Fprint(out, i18n.Text(s.Language, "worker.plain.consent"))
		answer, err := prompt()
		if err != nil {
			return plainReadError(ctx, err, s.Language)
		}
		if handled, updated := plainLanguageCommand(ctx, app, out, answer); handled {
			if ctx.Err() != nil {
				return nil
			}
			if updated != nil {
				s = *updated
			}
			continue
		}
		if answer != plainAgreement(s.Language) {
			fmt.Fprintln(out, i18n.Text(s.Language, "worker.plain.declined"))
			return nil
		}
		break
	}
	fmt.Fprint(out, i18n.Text(s.Language, "worker.plain.key"))
	key, err := password()
	fmt.Fprintln(out)
	if err != nil {
		clear(key)
		return plainReadError(ctx, err, s.Language)
	}
	err = app.ConsentAndConnect(key)
	clear(key)
	if err != nil {
		fmt.Fprintln(out, client.ErrorText(err, s.Language))
		return errors.New(client.ErrorText(err, s.Language))
	}
	fmt.Fprintln(out, i18n.Text(s.Language, "worker.plain.connecting"))
	reporter := plainReporter{seen: make(map[string]string)}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	line := commandLine()
	confirming := false
	for {
		s = app.Snapshot()
		reporter.print(out, s)
		if s.State == "disconnected" || s.State == "error" {
			reporter.history(out, s, 20)
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case r := <-line:
			command := strings.TrimSpace(string(r.data))
			clear(r.data)
			if r.err != nil {
				return plainReadError(ctx, r.err, s.Language)
			}
			if handled, updated := plainLanguageCommand(ctx, app, out, command); handled {
				if ctx.Err() != nil {
					return nil
				}
				confirming = false
				if updated != nil {
					s = *updated
				}
				printPlainAuthorization(out, s)
				fmt.Fprintln(out, i18n.Text(s.Language, "worker.plain.help"))
				line = commandLine()
				continue
			}
			if confirming {
				if command == plainAgreement(s.Language) {
					app.Disconnect("local_closed")
				}
				confirming = false
			} else {
				words := strings.Fields(command)
				if len(words) > 0 {
					words[0] = strings.ToLower(words[0])
					switch words[0] {
					case "d":
						fmt.Fprintln(out, i18n.Text(s.Language, "worker.plain.disconnect"))
						confirming = true
					case "h":
						reporter.history(out, s, 100)
					case "?":
						fmt.Fprintln(out, i18n.Text(s.Language, "worker.plain.help"))
					case "c", "l":
						reporter.taskCommand(app, out, s, words)
					default:
						fmt.Fprintln(out, i18n.Text(s.Language, "worker.plain.unknown"))
					}
				}
			}
			line = commandLine()
		}
	}
}

func plainAgreement(language i18n.Language) string {
	if language == i18n.English {
		return "AGREE"
	}
	return "同意"
}

func printPlainAuthorization(out io.Writer, s client.Snapshot) {
	for _, line := range consentLines(s, time.Now()) {
		fmt.Fprintln(out, line)
	}
}

// This function is only called at nonsecret prompts. Password input always
// travels directly from ReadPassword to ConsentAndConnect and is cleared.
func plainLanguageCommand(ctx context.Context, app *client.App, out io.Writer, command string) (bool, *client.Snapshot) {
	words := strings.Fields(command)
	if len(words) == 0 || words[0] != "lang" {
		return false, nil
	}
	if ctx.Err() != nil {
		return true, nil
	}
	if len(words) != 2 {
		fmt.Fprintln(out, i18n.Text(app.Language(), "worker.plain.language_help"))
		return true, nil
	}
	language, err := i18n.Parse(words[1])
	if err != nil {
		fmt.Fprintln(out, i18n.Text(app.Language(), "common.error.language"))
		return true, nil
	}
	// Wait serially for each explicit choice, but let cancellation end the
	// prompt while a filesystem callback or snapshot is still pending.
	completed := make(chan client.Snapshot, 1)
	go func() { _ = app.SetLanguage(language); completed <- app.Snapshot() }()
	select {
	case <-ctx.Done():
		return true, nil
	case snapshot := <-completed:
		if ctx.Err() != nil {
			return true, nil
		}
		// Only the prompt thread writes output, never a late save completion.
		// The callback error may contain local paths/secrets; use safe Warning.
		if snapshot.Warning != "" {
			fmt.Fprintln(out, client.SafeText(snapshot.Warning))
		}
		return true, &snapshot
	}
}

func (p *plainReporter) taskCommand(app *client.App, out io.Writer, s client.Snapshot, words []string) {
	number := 0
	if len(words) == 2 {
		number, _ = strconv.Atoi(words[1])
	}
	if number < 1 || number > len(p.ids) {
		fmt.Fprintln(out, i18n.Text(s.Language, "worker.plain.number"))
		p.history(out, s, 20)
		return
	}
	id := p.ids[number-1]
	for _, v := range s.Tasks {
		if v.Task.ID == id {
			if words[0] == "c" {
				if err := app.CancelTask(id); err != nil {
					fmt.Fprintln(out, client.ErrorText(err, s.Language))
				} else {
					fmt.Fprintln(out, i18n.Text(s.Language, "worker.plain.cancelled")+client.TaskTitle(v, s.Language))
				}
			} else {
				for _, log := range v.Logs {
					fmt.Fprintln(out, client.SafeText(log.Message))
				}
			}
			return
		}
	}
	fmt.Fprintln(out, i18n.Text(s.Language, "worker.plain.evicted"))
}

var errInputLong = errors.New("input too long")

func plainReadError(ctx context.Context, err error, language ...i18n.Language) error {
	lang := displayLanguage(language)
	if ctx.Err() != nil {
		return nil
	}
	if errors.Is(err, errInputLong) {
		return errors.New(i18n.Text(lang, "worker.plain.input_long"))
	}
	if errors.Is(err, io.EOF) {
		return errors.New(i18n.Text(lang, "worker.plain.input_closed"))
	}
	return errors.New(i18n.Text(lang, "worker.plain.input_error"))
}

type plainReporter struct {
	seen     map[string]string
	state    string
	language i18n.Language
	message  string
	numbers  map[string]int
	ids      []string
}

// Numbers belong to a task ID for the entire session, never to its mutable
// recent-first slice position. Eviction does not free a number for reuse.
func (p *plainReporter) number(id string) int {
	if p.numbers == nil {
		p.numbers = make(map[string]int)
	}
	if n, ok := p.numbers[id]; ok {
		return n
	}
	p.ids = append(p.ids, id)
	n := len(p.ids)
	p.numbers[id] = n
	return n
}

func (p *plainReporter) print(out io.Writer, s client.Snapshot) {
	if p.seen == nil {
		p.seen = make(map[string]string)
	}
	if p.state != s.State || p.language != s.Language {
		fmt.Fprintln(out, client.StateText(s.State, s.Language))
		p.state = s.State
		p.language = s.Language
	}
	message := strings.Join([]string{s.Notice, s.Warning, s.Error}, "\n")
	if message != p.message {
		for _, line := range []string{s.Notice, s.Warning, s.Error} {
			if line != "" {
				fmt.Fprintln(out, client.SafeText(line))
			}
		}
		p.message = message
	}

	for _, v := range s.Tasks {
		progress := taskProgress(v, s.Language)
		// Include state separately: a terminal result must print even if the last
		// progress event still says downloading/installing.
		signature := string(s.Language) + "|" + string(v.Task.State) + "|" + progress
		if p.seen[v.Task.ID] != signature {
			fmt.Fprintln(out, i18n.Text(s.Language, "worker.plain.task", p.number(v.Task.ID), client.TaskTitle(v, s.Language), progress, taskElapsed(v, time.Now(), s.Language)))
			p.seen[v.Task.ID] = signature
		}
	}
}
func (p *plainReporter) history(out io.Writer, s client.Snapshot, limit int) {
	fmt.Fprintln(out, i18n.Text(s.Language, "worker.plain.history", min(len(s.Tasks), limit), min(len(s.Tasks), 100)))
	for _, v := range worker.RecentTaskViews(s.Tasks, limit) {
		fmt.Fprintf(out, "%d. %s · %s\n", p.number(v.Task.ID), client.TaskTitle(v, s.Language), client.TaskResultText(v, s.Language))
	}
}
