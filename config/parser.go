package config

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Gap struct {
	Top, Bottom, Left, Right int
}
type ClientSpec struct {
	Name  string
	Class string
}
type KeySpec struct {
	Mods string
	Key  string
}

func (k KeySpec) ToXGB() string {
	var out []string
	for _, c := range k.Mods {
		switch c {
		case 'C':
			out = append(out, "Control")
		case 'M':
			out = append(out, "Mod1")
		case 'S':
			out = append(out, "Shift")
		case '4':
			out = append(out, "Mod4")
		}
	}
	out = append(out, k.Key)
	return strings.Join(out, "-")
}

type Config struct {
	BorderWidth int
	Snapdist    int
	Colors      map[string]string
	Gap         Gap
	Autogroups  map[ClientSpec]int
	Binds       map[KeySpec]string
	Commands    map[string]string
	Font        string // FIXME will we support Xft?
	Ignores     []string
	MouseBinds  map[string]KeySpec
	MoveAmount  int // default: 1
	Sticky      bool
}

type parseDecl struct {
	num int
	fn  func(cfg *Config, in []string) error
}

var parseMap = map[string]parseDecl{
	"autogroup": {2, func(cfg *Config, in []string) error {
		group, err := strconv.Atoi(in[0])
		if err != nil {
			return err
		}
		parts := strings.SplitN(in[1], ".", 2)
		var cs ClientSpec
		switch len(parts) {
		case 1:
			cs = ClientSpec{Class: parts[0]}
		case 2:
			cs = ClientSpec{Name: parts[0], Class: parts[1]}
		default:
			return fmt.Errorf("invalid clientspec %q", in[1])
		}
		cfg.Autogroups[cs] = group
		return nil
	}},

	"bind": {2, func(cfg *Config, in []string) error {
		parts := strings.SplitN(in[0], "-", 2)
		var key KeySpec
		switch len(parts) {
		case 1:
			key = KeySpec{Key: parts[0]}
		case 2:
			// TODO make sure the mod is valid
			key = KeySpec{Mods: parts[0], Key: parts[1]}
		default:
			return fmt.Errorf("invalid keyspec %q", in[0])
		}
		if in[1] == "unmap" {
			delete(cfg.Binds, key)
		} else {
			cfg.Binds[key] = in[1]
		}
		return nil
	}},

	"borderwidth": {1, func(cfg *Config, in []string) error {
		i, err := strconv.Atoi(in[0])
		if err != nil {
			return err
		}
		cfg.BorderWidth = i
		return nil
	}},

	"color": {2, func(cfg *Config, in []string) error {
		cfg.Colors[in[0]] = in[1]
		return nil
	}},

	"command": {2, func(cfg *Config, in []string) error {
		cfg.Commands[in[0]] = in[1]
		return nil
	}},

	"fontname": {1, func(cfg *Config, in []string) error {
		cfg.Font = in[0]
		return nil
	}},

	"gap": {4, func(cfg *Config, in []string) error {
		i, err := parseInts(in)
		if err != nil {
			return err
		}
		cfg.Gap.Top = i[0]
		cfg.Gap.Bottom = i[1]
		cfg.Gap.Left = i[2]
		cfg.Gap.Right = i[3]
		return nil
	}},

	"ignore": {1, func(cfg *Config, in []string) error {
		cfg.Ignores = append(cfg.Ignores, in[0])
		return nil
	}},

	"mousebind": {2, func(cfg *Config, in []string) error {
		parts := strings.SplitN(in[0], "-", 2)
		var key KeySpec
		switch len(parts) {
		case 1:
			key = KeySpec{Key: parts[0]}
		case 2:
			key = KeySpec{Mods: parts[0], Key: parts[1]}
		default:
			return fmt.Errorf("invalid mousepec %q", in[0])
		}
		if in[1] == "unmap" {
			for k, v := range cfg.MouseBinds {
				if v == key {
					delete(cfg.MouseBinds, k)
					break
				}
			}
		} else {
			cfg.MouseBinds[in[1]] = key
		}
		return nil
	}},

	"moveamount": {1, func(cfg *Config, in []string) error {
		i, err := strconv.Atoi(in[0])
		if err != nil {
			return err
		}
		cfg.MoveAmount = i
		return nil
	}},

	"snapdist": {1, func(cfg *Config, in []string) error {
		i, err := strconv.Atoi(in[0])
		if err != nil {
			return err
		}
		cfg.Snapdist = i
		return nil
	}},

	"sticky": {1, func(cfg *Config, in []string) error {
		switch in[0] {
		case "yes":
			cfg.Sticky = true
		case "no":
			cfg.Sticky = false
		default:
			return fmt.Errorf("invalid value %q for sticky", in[0])
		}
		return nil
	}},
}

func Parse(r io.Reader) (*Config, error) {
	cfg := &Config{}
	cfg.Autogroups = make(map[ClientSpec]int)
	cfg.Binds = make(map[KeySpec]string)
	cfg.Colors = make(map[string]string)
	cfg.Commands = make(map[string]string)
	cfg.MouseBinds = make(map[string]KeySpec)
	cfg.MoveAmount = 1

	cnt, _ := ioutil.ReadAll(r)
	_, ch := lex(string(cnt))
	for {
		command, ok := <-ch
		if !ok {
			return cfg, errors.New("internal error")
		}

		if command.typ == itemEOF {
			return cfg, nil
		}
		if command.typ == itemTerminator {
			continue
		}
		if command.typ != itemString {
			return cfg, errors.New("unexpected token " + command.String())
		}
		decl, ok := parseMap[command.val]
		if !ok {
			return cfg, errors.New("unknown option " + command.val)
		}
		in, err := expect(ch, decl.num)
		if err != nil {
			return cfg, err
		}
		err = decl.fn(cfg, in)
		if err != nil {
			return cfg, err
		}
	}
}

func parseInts(in []string) ([]int, error) {
	out := make([]int, len(in))
	var err error
	for i, s := range in {
		out[i], err = strconv.Atoi(s)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func expect(ch chan item, num int) ([]string, error) {
	var ret []string
	for i := 0; i < num; i++ {
		val := <-ch
		if val.typ == itemError {
			return ret, errors.New(val.val)
		}

		if val.typ == itemTerminator || val.typ == itemEOF {
			return ret, io.EOF
		}

		ret = append(ret, val.val)
	}

	val := <-ch
	if val.typ != itemTerminator {
		return ret, errors.New("unexpected token " + val.typ.String())
	}

	return ret, nil
}

type lexer struct {
	input             string
	start             int
	pos               int
	width             int
	items             chan item
	lastWasTerminator bool
}
type itemType int

const (
	itemError itemType = iota
	itemString
	itemTerminator
	itemEOF
)

func (i itemType) String() string {
	switch i {
	case itemError:
		return "error"
	case itemString:
		return "string"
	case itemTerminator:
		return "terminator"
	case itemEOF:
		return "eof"
	default:
		return ""
	}
}

const eof = -1

type item struct {
	typ itemType
	val string
}

func (i item) String() string {
	switch i.typ {
	case itemEOF:
		return "EOF"
	case itemError:
		return i.val
	}
	return fmt.Sprintf("(%s) %q", i.typ, i.val)
}

type stateFn func(*lexer) stateFn

func lex(input string) (*lexer, chan item) {
	l := &lexer{
		input: input,
		items: make(chan item),
	}
	go l.run()
	return l, l.items
}

func (l *lexer) run() {
	for state := lexText; state != nil; {
		state = state(l)
	}
	close(l.items)
}

func (l *lexer) emit(t itemType) {
	l.lastWasTerminator = t == itemTerminator
	l.items <- item{t, l.input[l.start:l.pos]}
	l.start = l.pos
}

func (l *lexer) next() (rune rune) {
	if l.pos >= len(l.input) {
		l.width = 0
		return eof
	}
	rune, l.width = utf8.DecodeRuneInString(l.input[l.pos:])
	l.pos += l.width
	return rune
}

func (l *lexer) ignore() {
	l.start = l.pos
}

func (l *lexer) backup() {
	l.pos -= l.width
}

func (l *lexer) peek() rune {
	rune := l.next()
	l.backup()
	return rune
}

func (l *lexer) accept(valid string) bool {
	if strings.ContainsRune(valid, l.next()) {
		return true
	}
	l.backup()
	return false
}

func (l *lexer) acceptRun(valid string) {
	for strings.ContainsRune(valid, l.next()) {
	}
	l.backup()
}

func (l *lexer) errorf(format string, args ...interface{}) stateFn {
	l.items <- item{itemError, fmt.Sprintf(format, args...)}
	return nil
}

func lexText(l *lexer) stateFn {
	for {
		r := l.next()
		if r == eof {
			break
		}

		if r == '#' {
			return lexComment
		}

		// TODO is this right?
		if r == ' ' || r == '\t' {
			l.ignore()
			continue
		}

		if r == '\n' {
			if l.lastWasTerminator {
				l.ignore()
			} else {
				l.emit(itemTerminator)
			}
		}

		return lexString

	}
	l.emit(itemEOF)
	return nil
}

func lexString(l *lexer) stateFn {
	quoted := false
	defer func() {
		if l.input[l.start:l.pos] != "" {
			if quoted {
				l.start++
				l.pos--
			}
			l.emit(itemString)
			if quoted {
				l.pos++
				l.start = l.pos
			}
		}
	}()
	if l.input[l.pos-1] == '"' {
		quoted = true
	}
	escape := false
	multiline := false

	var r rune
loop:
	for r != eof {
		r = l.next()
		switch r {
		case '\\':
			if quoted {
				escape = !escape
			} else {
				multiline = true
			}
			// TODO multiline string
		case '"':
			if quoted && !escape {
				break loop
			}
		case ' ', '\t':
			if !quoted {
				l.backup()
				break loop
			}
		case '\n':
			if quoted || multiline {
				multiline = false
			} else {
				l.backup()
				break loop
			}
		case '#':
			if !quoted {
				l.backup()

				return lexComment
			}
		}
	}

	return lexText
}

func lexComment(l *lexer) stateFn {
	for {
		r := l.next()
		if r == eof || r == '\n' {
			l.backup()
			// TODO can comments be multiline?
			break
		}
	}
	l.ignore()
	return lexText
}
