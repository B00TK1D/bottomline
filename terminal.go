package bottomline

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"
)

type ParamType int

const (
	ParamString ParamType = iota
	ParamInt
	ParamPath
)

type routeSegment struct {
	literal   string
	isParam   bool
	paramName string
	paramType ParamType
}

type route struct {
	segments []routeSegment
	handler  any
}

type Router struct {
	routes []*route
}

type Terminal struct {
	oldState             *term.State
	width                int
	height               int
	prompt               string
	input                []rune
	cursorPos            int
	history              []string
	historyPos           int
	historyBuf           string
	output               chan string
	commands             chan string
	done                 chan struct{}
	wg                   sync.WaitGroup
	mu                   sync.Mutex
	scrollMode           bool
	router               *Router
	lastTab              time.Time
	completionsShown     bool
	completionsLineAbove bool
	handlerRunning       bool
	inputMode            bool
	inputResult          chan string
	inputSavedPrompt     string
	rawInputBuf          []byte
	inputPromptDrawn     bool // true after first render in input mode (cursor is on last line of prompt)
}

func New(prompt string) (*Terminal, error) {
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}

	width, height, err := term.GetSize(fd)
	if err != nil {
		term.Restore(fd, oldState)
		return nil, err
	}

	t := &Terminal{
		oldState:   oldState,
		width:      width,
		height:     height,
		prompt:     prompt,
		input:      make([]rune, 0),
		cursorPos:  0,
		history:    make([]string, 0),
		historyPos: 0,
		output:     make(chan string, 100),
		commands:   make(chan string, 10),
		done:       make(chan struct{}),
		scrollMode: false,
		router:     &Router{routes: make([]*route, 0)},
	}

	t.wg.Add(2)
	go t.readInput()
	go t.handleOutput()

	t.renderPrompt()

	return t, nil
}

func (t *Terminal) Register(path string, handler any) {
	t.mu.Lock()
	defer t.mu.Unlock()

	segments := parseRoute(path)
	t.router.routes = append(t.router.routes, &route{
		segments: segments,
		handler:  handler,
	})
}

func parseRoute(path string) []routeSegment {
	if len(path) == 0 {
		return nil
	}

	if path[0] == '/' {
		path = path[1:]
	}

	parts := splitBySlash(path)
	segments := make([]routeSegment, 0, len(parts))

	for _, part := range parts {
		if len(part) == 0 {
			continue
		}

		if part[0] == ':' {
			paramName := part[1:]
			paramType := ParamString

			if len(paramName) > 4 && paramName[len(paramName)-4:] == ":int" {
				paramName = paramName[:len(paramName)-4]
				paramType = ParamInt
			} else if len(paramName) > 5 && paramName[len(paramName)-5:] == ":path" {
				paramName = paramName[:len(paramName)-5]
				paramType = ParamPath
			}

			segments = append(segments, routeSegment{
				isParam:   true,
				paramName: paramName,
				paramType: paramType,
			})
		} else {
			segments = append(segments, routeSegment{
				literal: part,
				isParam: false,
			})
		}
	}

	return segments
}

func splitBySlash(s string) []string {
	parts := make([]string, 0)
	current := ""

	for _, r := range s {
		if r == '/' {
			if len(current) > 0 {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(r)
		}
	}

	if len(current) > 0 {
		parts = append(parts, current)
	}

	return parts
}

func (t *Terminal) Print(a ...any) {
	select {
	case t.output <- fmt.Sprint(a...):
	case <-t.done:
	}
}

func (t *Terminal) Println(a ...any) {
	select {
	case t.output <- fmt.Sprintln(a...):
	case <-t.done:
	}
}

func (t *Terminal) Printf(format string, a ...any) {
	select {
	case t.output <- fmt.Sprintf(format, a...):
	case <-t.done:
	}
}

// Stream reads from r until closed (EOF) and prints each line via Println.
func (t *Terminal) Stream(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		t.Println(scanner.Text())
	}
}

func (t *Terminal) Commands() <-chan string {
	return t.commands
}

func (t *Terminal) SetPrompt(prompt string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prompt = prompt
	t.renderPrompt()
}

func (t *Terminal) Input(prompt string) string {
	t.mu.Lock()
	t.inputMode = true
	t.inputPromptDrawn = false // first draw: cursor not yet on last line of prompt
	t.inputSavedPrompt = t.prompt
	//t.prompt = normalizePrompt(prompt)
	t.prompt = prompt
	t.input = make([]rune, 0)
	t.cursorPos = 0
	t.rawInputBuf = t.rawInputBuf[:0]
	t.inputResult = make(chan string)
	t.renderPrompt()
	os.Stdout.Sync()
	t.mu.Unlock()
	return <-t.inputResult
}

func (t *Terminal) InputInt(prompt string) int {
	for {
		input := t.Input(prompt)
		value, err := strconv.Atoi(strings.TrimSpace(input))
		if err == nil {
			return value
		}
		t.Println("Invalid input, must be an integer")
	}
}

func (t *Terminal) InputSelect(prompt string, options []string) string {
	for i, opt := range options {
		prompt += fmt.Sprintf("\r\n - %2d: \033[0;37m%s\033[0m", i, opt)
	}
	prompt += fmt.Sprintf("\r\nSelect an option (0-%d, default 0): ", len(options)-1)

	for {
		input := t.InputInt(prompt)
		if input >= 0 && input < len(options) {
			return options[input]
		}
		t.Println("Invalid option, please try again")
	}
}

func (t *Terminal) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()

	fmt.Print("\033[2J")
	fmt.Printf("\033[1;%dr", t.height-1)
	fmt.Printf("\033[%d;1H", t.height-1)
	fmt.Print("\n")
	t.scrollMode = true
	t.renderPrompt()
}

func (t *Terminal) Exit() {
	select {
	case <-t.done:
	default:
		close(t.done)
	}
}

func (t *Terminal) Close() {
	t.Exit()
	t.wg.Wait()
	if t.scrollMode {
		fmt.Print("\033[r")
	}
	term.Restore(int(os.Stdin.Fd()), t.oldState)
	fmt.Print("\n")
}

func (t *Terminal) readInput() {
	defer t.wg.Done()
	defer close(t.commands)

	fd := int(os.Stdin.Fd())

	for {
		select {
		case <-t.done:
			return
		default:
		}

		syscall.SetNonblock(fd, true)
		buf := make([]byte, 16)
		n, err := syscall.Read(fd, buf)
		syscall.SetNonblock(fd, false)

		if err != nil {
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			if err == syscall.EINTR {
				continue
			}
			return
		}

		if n == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		t.rawInputBuf = append(t.rawInputBuf, buf[:n]...)

		t.mu.Lock()
		t.rawInputBuf = t.processInputBuffer(t.rawInputBuf)
		t.mu.Unlock()
	}
}

func (t *Terminal) processInputBuffer(buf []byte) []byte {
	for len(buf) > 0 {
		if buf[0] == 27 && len(buf) >= 2 {
			seqLen := t.detectEscapeSequenceLength(buf)
			if seqLen == 0 {
				return buf
			}
			if len(buf) < seqLen {
				return buf
			}
			t.handleEscapeSequence(string(buf[1:seqLen]))
			buf = buf[seqLen:]
		} else {
			t.handleByte(buf[0])
			buf = buf[1:]
		}
	}
	return buf
}

func (t *Terminal) detectEscapeSequenceLength(buf []byte) int {
	if len(buf) < 2 {
		return 0
	}
	if buf[1] == '[' {
		for i := 2; i < len(buf) && i < 8; i++ {
			if buf[i] >= 'A' && buf[i] <= 'Z' || buf[i] >= 'a' && buf[i] <= 'z' || buf[i] == '~' {
				return i + 1
			}
		}
		if len(buf) >= 8 {
			return 8
		}
		return 0
	}
	return 2
}

func (t *Terminal) handleByte(b byte) {
	r := rune(b)
	switch r {
	case '\r', '\n':
		t.handleEnter()
	case '\t':
		t.handleTab()
	case 127, 8:
		t.handleBackspace()
	case 3:
		select {
		case <-t.done:
		default:
			close(t.done)
		}
	default:
		if r >= 32 && r < 127 || r >= 128 {
			t.insertRune(r)
		}
	}
}

func (t *Terminal) handleTab() {
	if t.inputMode || t.router == nil {
		return
	}

	now := time.Now()
	isDoubleTap := now.Sub(t.lastTab) < 500*time.Millisecond
	t.lastTab = now

	inputStr := string(t.input[:t.cursorPos])
	parts := splitInput(inputStr)

	matches := t.findMatches(parts)

	if isDoubleTap && len(matches) > 0 {
		t.showCompletions(matches)
		return
	}

	if len(matches) == 0 {
		return
	}

	if len(matches) == 1 {
		t.applyCompletion(parts, matches[0], true)
	} else {
		commonPrefix := findCommonPrefix(matches)
		currentPrefix := ""
		if len(parts) > 0 {
			currentPrefix = parts[len(parts)-1]
		}
		if len(commonPrefix) > len(currentPrefix) {
			t.applyCompletion(parts, commonPrefix, false)
		}
	}
}

func findCommonPrefix(matches []string) string {
	if len(matches) == 0 {
		return ""
	}
	if len(matches) == 1 {
		return matches[0]
	}

	prefix := matches[0]
	for _, match := range matches[1:] {
		for i := 0; i < len(prefix) && i < len(match); i++ {
			if prefix[i] != match[i] {
				prefix = prefix[:i]
				break
			}
		}
		if len(match) < len(prefix) {
			prefix = prefix[:len(match)]
		}
	}
	return prefix
}

func (t *Terminal) showCompletions(matches []string) {
	completionText := "\033[90m"
	for i, match := range matches {
		if i > 0 {
			completionText += "  "
		}
		completionText += match
	}
	completionText += "\033[0m"

	if t.scrollMode {
		fmt.Print("\0337")
		if t.completionsShown {
			fmt.Printf("\033[%d;1H\033[K", t.height-1)
		} else {
			fmt.Printf("\033[%d;1H", t.height-1)
		}
		fmt.Print(completionText)
		fmt.Printf("\033[%d;1H", t.height)
		fmt.Print("\0338")
	} else {
		if t.completionsShown {
			fmt.Print("\033[1A\r\033[K")
		} else {
			fmt.Print("\r\033[K")
		}
		fmt.Print(completionText)
		fmt.Print("\n")
	}

	t.completionsShown = true
	t.completionsLineAbove = true
	t.renderPrompt()
}

func (t *Terminal) applyCompletion(parts []string, match string, addSpace bool) {
	var newInput string
	suffix := ""
	if addSpace {
		suffix = " "
	}

	if len(parts) == 0 {
		newInput = match + suffix
	} else {
		parts[len(parts)-1] = match
		newInput = joinParts(parts) + suffix
	}

	t.input = []rune(newInput)
	t.cursorPos = len(t.input)
	t.renderPrompt()
}

func (t *Terminal) findMatches(parts []string) []string {
	if t.router == nil {
		return nil
	}

	matches := make(map[string]bool)

	for _, r := range t.router.routes {
		if len(parts) == 0 {
			if len(r.segments) > 0 {
				if r.segments[0].isParam {
					matches[formatParam(r.segments[0])] = true
				} else {
					matches[r.segments[0].literal] = true
				}
			}
			continue
		}

		segIdx := 0
		matched := true

		for i := 0; i < len(parts)-1 && segIdx < len(r.segments); i++ {
			if r.segments[segIdx].isParam {
				if !validateParam(parts[i], r.segments[segIdx].paramType) {
					matched = false
					break
				}
			} else if r.segments[segIdx].literal != parts[i] {
				matched = false
				break
			}
			segIdx++
		}

		if matched && segIdx < len(r.segments) {
			prefix := parts[len(parts)-1]

			if r.segments[segIdx].isParam {
				if r.segments[segIdx].paramType == ParamPath {
					pathMatches := t.findPathMatches(prefix)
					for _, pm := range pathMatches {
						matches[pm] = true
					}
				} else {
					paramStr := formatParam(r.segments[segIdx])
					if len(prefix) == 0 || hasPrefix(paramStr, prefix) {
						matches[paramStr] = true
					}
				}
			} else if hasPrefix(r.segments[segIdx].literal, prefix) {
				matches[r.segments[segIdx].literal] = true
			}
		}
	}

	result := make([]string, 0, len(matches))
	for m := range matches {
		result = append(result, m)
	}
	sort.Strings(result)
	return result
}

func formatParam(seg routeSegment) string {
	switch seg.paramType {
	case ParamInt:
		return "<int>"
	case ParamPath:
		return "<path>"
	default:
		return "<string>"
	}
}

func validateParam(value string, ptype ParamType) bool {
	switch ptype {
	case ParamInt:
		for _, r := range value {
			if r < '0' || r > '9' {
				return false
			}
		}
		return len(value) > 0
	case ParamString, ParamPath:
		return true
	}
	return true
}

func (t *Terminal) findPathMatches(prefix string) []string {
	dir := "."
	pattern := prefix

	lastSlash := -1
	for i := len(prefix) - 1; i >= 0; i-- {
		if prefix[i] == '/' {
			lastSlash = i
			break
		}
	}

	if lastSlash >= 0 {
		dir = prefix[:lastSlash+1]
		if dir == "" {
			dir = "/"
		}
		pattern = prefix[lastSlash+1:]
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	matches := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if len(pattern) == 0 || hasPrefix(name, pattern) {
			fullPath := name
			if dir != "." {
				if dir == "/" {
					fullPath = "/" + name
				} else {
					fullPath = dir + name
				}
			}
			if entry.IsDir() {
				fullPath += "/"
			}
			matches = append(matches, fullPath)
		}
	}

	return matches
}

func (t *Terminal) getSuggestion() string {
	if t.inputMode || t.router == nil {
		return ""
	}

	inputStr := string(t.input[:t.cursorPos])
	parts := splitInput(inputStr)

	matches := t.findMatches(parts)
	if len(matches) != 1 {
		return ""
	}

	if matches[0] == "<string>" || matches[0] == "<int>" || matches[0] == "<path>" {
		return ""
	}

	prefix := ""
	if len(parts) > 0 {
		prefix = parts[len(parts)-1]
	}

	if len(matches[0]) <= len(prefix) {
		return ""
	}

	return matches[0][len(prefix):]
}

func splitInput(s string) []string {
	if len(s) == 0 {
		return []string{}
	}

	parts := make([]string, 0)
	current := ""

	for _, r := range s {
		if r == ' ' {
			if len(current) > 0 {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(r)
		}
	}

	if len(current) > 0 || (len(s) > 0 && s[len(s)-1] == ' ') {
		parts = append(parts, current)
	}

	return parts
}

func joinParts(parts []string) string {
	result := ""
	for i, part := range parts {
		if i > 0 {
			result += " "
		}
		result += part
	}
	return result
}

func hasPrefix(s, prefix string) bool {
	if len(prefix) > len(s) {
		return false
	}
	return s[:len(prefix)] == prefix
}

func (t *Terminal) handleEnter() {
	if t.inputMode {
		t.inputResult <- string(t.input)
		t.inputMode = false
		t.prompt = t.inputSavedPrompt
		t.input = make([]rune, 0)
		t.cursorPos = 0
		return
	}
	cmd := strings.TrimRight(string(t.input), " \t")
	if cmd != "" {
		t.history = append(t.history, cmd)
		t.historyPos = len(t.history)

		if t.dispatch(cmd) {
			return
		}
		select {
		case t.commands <- cmd:
		case <-t.done:
			return
		}
	}
	t.input = make([]rune, 0)
	t.cursorPos = 0
	t.historyBuf = ""
	t.renderPrompt()
}

func (t *Terminal) printOutput(data string) {
	t.clearCompletions(false)
	if t.inputMode {
		n := t.promptLineCount()
		if n == 1 {
			// Single-line prompt: insert a new line above the prompt, print message there, then redraw the prompt.
			// This keeps "> confirm" and the prompt intact and appends each message (messages grow upward).
			fmt.Print("\033[1A")  // move up to the line above the prompt (e.g. the "> confirm" line)
			fmt.Print("\033[L")   // insert one blank line (pushes that line and prompt down; cursor stays on the new line)
			fmt.Print("\r\033[K") // clear the new line and ensure column 0
			fmt.Print(data)
			if !strings.HasSuffix(data, "\n") {
				fmt.Print("\n")
			}
			// Cursor is on the line below the message; move down to the prompt line and redraw.
			fmt.Print("\033[1B")
			fmt.Print("\r\033[K")
			t.renderPromptContent()
		} else {
			// Multi-line prompt: clear the prompt block, print output in its place, then redraw prompt below.
			t.clearPromptBlock()
			fmt.Print(data)
			if !strings.HasSuffix(data, "\n") {
				fmt.Print("\n")
			}
			fmt.Print("\r")
			t.renderPromptContent()
		}
	} else if t.scrollMode {
		fmt.Print("\0337")
		fmt.Printf("\033[%d;1H", t.height-1)
		fmt.Print(data)
		fmt.Printf("\033[%d;1H\033[K", t.height)
		if t.handlerRunning {
			fmt.Print("\r")
		} else {
			fmt.Print("\0338")
		}
	} else {
		fmt.Print("\r\033[K")
		fmt.Print(data)
		if t.handlerRunning {
			fmt.Print("\r")
		}
	}
	if !t.handlerRunning && !t.inputMode {
		t.renderPrompt()
	}
}

func (t *Terminal) dispatch(cmd string) bool {
	parts := splitInput(cmd)
	if len(parts) == 0 {
		return false
	}

	for _, r := range t.router.routes {
		if params, ok := matchRoute(r.segments, parts); ok {
			t.input = make([]rune, 0)
			t.cursorPos = 0
			t.historyBuf = ""
			t.handlerRunning = true
			t.printOutput(t.prompt + cmd + "\n")
			t.mu.Unlock()
			go func() {
				callHandler(t, r.handler, params)
				t.mu.Lock()
				t.handlerRunning = false
				t.renderPrompt()
				t.mu.Unlock()
			}()
			t.mu.Lock()
			return true
		}
	}

	return false
}

func matchRoute(segments []routeSegment, parts []string) ([]any, bool) {
	if len(segments) != len(parts) {
		return nil, false
	}

	params := make([]any, 0)

	for i, seg := range segments {
		if seg.isParam {
			switch seg.paramType {
			case ParamInt:
				val := 0
				for _, r := range parts[i] {
					if r < '0' || r > '9' {
						return nil, false
					}
					val = val*10 + int(r-'0')
				}
				params = append(params, val)
			case ParamString, ParamPath:
				params = append(params, parts[i])
			}
		} else {
			if seg.literal != parts[i] {
				return nil, false
			}
		}
	}

	return params, true
}

func callHandler(t *Terminal, handler any, params []any) {
	if handler == nil {
		return
	}

	switch h := handler.(type) {
	case func(*Terminal):
		h(t)
	case func(*Terminal, string):
		if len(params) >= 1 {
			if s, ok := params[0].(string); ok {
				h(t, s)
			}
		}
	case func(*Terminal, int):
		if len(params) >= 1 {
			if i, ok := params[0].(int); ok {
				h(t, i)
			}
		}
	case func(*Terminal, string, string):
		if len(params) >= 2 {
			if s1, ok := params[0].(string); ok {
				if s2, ok := params[1].(string); ok {
					h(t, s1, s2)
				}
			}
		}
	case func(*Terminal, string, int):
		if len(params) >= 2 {
			if s, ok := params[0].(string); ok {
				if i, ok := params[1].(int); ok {
					h(t, s, i)
				}
			}
		}
	}
}

func (t *Terminal) handleBackspace() {
	if t.cursorPos > 0 {
		t.input = append(t.input[:t.cursorPos-1], t.input[t.cursorPos:]...)
		t.cursorPos--
		t.clearCompletions(true)
		t.renderPrompt()
	}
}

func (t *Terminal) handleDelete() {
	if t.cursorPos < len(t.input) {
		t.input = append(t.input[:t.cursorPos], t.input[t.cursorPos+1:]...)
		t.renderPrompt()
	}
}

func (t *Terminal) clearCompletions(moveBack bool) {
	useLine := t.completionsShown || t.completionsLineAbove
	if !useLine {
		return
	}
	t.completionsShown = false
	if !moveBack {
		t.completionsLineAbove = false
	}
	if t.scrollMode {
		fmt.Printf("\033[%d;1H\033[K", t.height-1)
	} else {
		fmt.Print("\033[1A\r\033[K")
		if moveBack {
			fmt.Print("\033[1B")
		}
	}
}

func (t *Terminal) insertRune(r rune) {
	if t.cursorPos == len(t.input) {
		t.input = append(t.input, r)
	} else {
		t.input = append(t.input[:t.cursorPos], append([]rune{r}, t.input[t.cursorPos:]...)...)
	}
	t.cursorPos++
	t.clearCompletions(true)
	t.renderPrompt()
}

func (t *Terminal) cursorLeft() {
	if t.cursorPos > 0 {
		t.cursorPos--
		t.renderPrompt()
	}
}

func (t *Terminal) cursorRight() {
	if t.cursorPos < len(t.input) {
		t.cursorPos++
		t.renderPrompt()
	}
}

func (t *Terminal) cursorHome() {
	t.cursorPos = 0
	t.renderPrompt()
}

func (t *Terminal) cursorEnd() {
	t.cursorPos = len(t.input)
	t.renderPrompt()
}

func (t *Terminal) historyUp() {
	if len(t.history) == 0 {
		return
	}
	if t.historyPos == len(t.history) {
		t.historyBuf = string(t.input)
	}
	if t.historyPos > 0 {
		t.historyPos--
		t.input = []rune(t.history[t.historyPos])
		t.cursorPos = len(t.input)
		t.renderPrompt()
	}
}

func (t *Terminal) historyDown() {
	if t.historyPos < len(t.history) {
		t.historyPos++
		if t.historyPos == len(t.history) {
			t.input = []rune(t.historyBuf)
		} else {
			t.input = []rune(t.history[t.historyPos])
		}
		t.cursorPos = len(t.input)
		t.renderPrompt()
	}
}

func (t *Terminal) handleEscapeSequence(seq string) {
	switch seq {
	case "[A":
		t.historyUp()
	case "[B":
		t.historyDown()
	case "[C":
		t.cursorRight()
	case "[D":
		t.cursorLeft()
	case "[3~":
		t.handleDelete()
	case "[H":
		t.cursorHome()
	case "[F":
		t.cursorEnd()
	}
}

func (t *Terminal) handleOutput() {
	defer t.wg.Done()
	for {
		select {
		case <-t.done:
			return
		case data := <-t.output:
			t.mu.Lock()
			t.printOutput(data)
			t.mu.Unlock()
		}
	}
}

// promptLineCount returns the number of lines the prompt occupies when displayed.
func (t *Terminal) promptLineCount() int {
	return strings.Count(t.prompt, "\n") + 1
}

// clearPromptBlock assumes the cursor is on the last line of the prompt block. It moves up,
// clears all prompt lines, and leaves the cursor at the start of the first line.
func (t *Terminal) clearPromptBlock() {
	n := t.promptLineCount()
	if n <= 0 {
		return
	}
	if n > 1 {
		fmt.Printf("\033[%dA", n-1)
	}
	for i := range n {
		fmt.Print("\r\033[K")
		if i < n-1 {
			fmt.Print("\033[1B")
		}
	}
	if n > 1 {
		fmt.Printf("\033[%dA", n-1)
	}
}

// renderPromptContent writes the prompt text, input buffer, suggestion, and cursor positioning.
// Caller is responsible for positioning the cursor and clearing lines when needed.
func (t *Terminal) renderPromptContent() {
	fmt.Print(t.prompt)
	fmt.Print(string(t.input))

	suggestion := t.getSuggestion()
	if suggestion != "" && t.cursorPos == len(t.input) {
		fmt.Print("\033[90m")
		fmt.Print(suggestion)
		fmt.Print("\033[0m")
	}

	if t.cursorPos < len(t.input) {
		back := len(t.input) - t.cursorPos
		if suggestion != "" {
			back += len(suggestion)
		}
		fmt.Printf("\033[%dD", back)
	} else if suggestion != "" {
		fmt.Printf("\033[%dD", len(suggestion))
	}
}

func (t *Terminal) renderPrompt() {
	n := t.promptLineCount()
	if t.scrollMode {
		row := t.height - n + 1
		fmt.Printf("\033[%d;1H", row)
		for i := range n {
			fmt.Print("\033[K")
			if i < n-1 {
				fmt.Print("\033[1B")
			}
		}
		fmt.Printf("\033[%d;1H", row)
		t.renderPromptContent()
		return
	}
	// In input mode, first draw: cursor is on line after command, not on last line of prompt.
	// Only use clearPromptBlock when redrawing (cursor is on last line of prompt).
	if t.inputMode && !t.inputPromptDrawn {
		fmt.Print("\r\033[K")
		t.renderPromptContent()
		t.inputPromptDrawn = true
		return
	}
	t.clearPromptBlock()
	t.renderPromptContent()
	if t.inputMode {
		t.inputPromptDrawn = true
	}
}
