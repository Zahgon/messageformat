// SPDX-License-Identifier: Apache-2.0
// Ported from the MessageFormat 2 package; see mf2/messageformat/LICENSE.
//
// This is a Go port of the MessageFormat 2 parser and resolver shipped by
// messageformat 4.1.0. It intentionally has no external dependencies so that
// the command can be built in an offline module cache.
package mf

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Request struct {
	Locales any            `json:"locales"`
	Source  string         `json:"source"`
	Params  map[string]any `json:"params"`
	Options map[string]any `json:"options"`
}
type Error struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Message string `json:"message"`
	Source  string `json:"source,omitempty"`
	Start   *int   `json:"start,omitempty"`
	End     *int   `json:"end,omitempty"`
}
type Response struct {
	Output *string `json:"output"`
	Parts  any     `json:"parts"`
	Errors []Error `json:"errors"`
	Status int     `json:"status"`
}
type Part map[string]any

func InvalidInput(err error) Response {
	return Response{nil, nil, []Error{{Name: "TypeError", Type: "invalid-input", Message: err.Error()}}, 2}
}

type syntaxError struct {
	typ        string
	start, end int
	expected   string
}

func (e syntaxError) Error() string {
	if e.expected != "" {
		return fmt.Sprintf("Missing %s at %d", e.expected, e.start)
	}
	if e.start < 0 {
		return e.typ
	}
	return fmt.Sprintf("%s at %d", e.typ, e.start)
}

type value struct{ kind, raw string }
type option struct {
	name string
	val  value
}
type expr struct {
	kind, name string
	arg        *value
	opts       []option
	attrs      []option
}
type element struct {
	text *string
	ex   *expr
}
type decl struct {
	kind, name string
	ex         expr
}
type variant struct {
	keys    []string
	catch   []bool
	pattern []element
}
type message struct {
	decls     []decl
	selectors []string
	variants  []variant
	pattern   []element
}
type parser struct {
	src string
	pos int
}

func Format(req Request) Response {
	p := parser{src: req.Source}
	m, err := p.parse()
	if err != nil {
		se := err.(syntaxError)
		st, en := se.start, se.end
		name := "MessageSyntaxError"
		switch se.typ {
		case "duplicate-declaration", "duplicate-variant", "key-mismatch", "missing-fallback", "missing-selector-annotation":
			name = "MessageDataModelError"
		}
		return Response{nil, nil, []Error{{Name: name, Type: se.typ, Message: se.Error(), Start: &st, End: &en}}, 2}
	}
	r := resolver{req: req, scope: map[string]any{}, msg: m, errors: []Error{}}
	for k, v := range req.Params {
		r.scope[k] = v
	}
	for _, d := range m.decls {
		var input any
		if d.kind == "input" {
			input = lookupMap(req.Params, d.name)
		}
		r.scope[d.name] = lazy{d: d, input: input}
	}
	pat := m.pattern
	if len(m.selectors) > 0 {
		pat = r.selectPattern()
	}
	parts := []Part{}
	var b strings.Builder
	for _, el := range pat {
		if el.text != nil {
			b.WriteString(*el.text)
			parts = append(parts, Part{"type": "text", "value": *el.text})
			continue
		}
		ps, s := r.resolveExpr(*el.ex)
		b.WriteString(s)
		parts = append(parts, ps...)
	}
	out := b.String()
	return Response{&out, parts, r.errors, 0}
}

func (p *parser) parse() (message, error) {
	m := message{}
	p.ws()
	for p.peek(".") && !p.peek(".match") {
		start := p.pos
		if p.take(".input") {
			p.ws()
			ex, e := p.expression(false)
			if e != nil {
				return m, e
			}
			if ex.arg == nil || ex.arg.kind != "var" {
				return m, p.err("bad-input-expression", start, p.pos, "")
			}
			m.decls = append(m.decls, decl{"input", ex.arg.raw, ex})
		} else if p.take(".local") {
			if !p.wsReq() {
				return m, p.missing("' '")
			}
			if !p.take("$") {
				return m, p.missing("$")
			}
			n, e := p.name()
			if e != nil {
				return m, e
			}
			p.ws()
			if !p.take("=") {
				return m, p.missing("=")
			}
			p.ws()
			ex, e := p.expression(false)
			if e != nil {
				return m, e
			}
			m.decls = append(m.decls, decl{"local", n, ex})
		} else {
			return m, p.err("parse-error", p.pos, p.pos+1, "")
		}
		p.ws()
	}
	if p.take(".match") {
		if !p.wsReq() {
			return m, p.missing("' '")
		}
		for p.peek("$") {
			p.pos++
			n, e := p.name()
			if e != nil {
				return m, e
			}
			m.selectors = append(m.selectors, n)
			if !p.wsReq() && p.peek("$") {
				return m, p.missing("' '")
			}
			p.ws()
		}
		if len(m.selectors) == 0 {
			return m, p.err("empty-token", p.pos, p.pos+1, "")
		}
		for p.pos < len(p.src) {
			v, e := p.variant(len(m.selectors))
			if e != nil {
				return m, e
			}
			m.variants = append(m.variants, v)
			p.ws()
		}
		if e := validate(m); e != nil {
			return m, e
		}
		return m, nil
	}
	quoted := len(m.decls) > 0 || p.peek("{{")
	if !quoted && p.pos > 0 {
		p.pos = 0
	}
	pat, e := p.pattern(quoted)
	if e != nil {
		return m, e
	}
	m.pattern = pat
	if quoted {
		p.ws()
		if p.pos < len(p.src) {
			return m, p.err("extra-content", p.pos, len(p.src), "")
		}
	}
	if e := validate(m); e != nil {
		return m, e
	}
	return m, nil
}
func validate(m message) error {
	seen := map[string]bool{}
	for _, d := range m.decls {
		if seen[d.name] {
			return syntaxError{"duplicate-declaration", -1, 0, ""}
		}
		seen[d.name] = true
	}
	if len(m.selectors) > 0 {
		for _, selector := range m.selectors {
			annotated := false
			for _, d := range m.decls {
				if d.name == selector && d.ex.name != "" {
					annotated = true
					break
				}
			}
			if !annotated {
				return syntaxError{"missing-selector-annotation", -1, -1, ""}
			}
		}
		fallback := false
		keys := map[string]bool{}
		for _, v := range m.variants {
			if len(v.keys) != len(m.selectors) {
				return syntaxError{"key-mismatch", -1, 0, ""}
			}
			all := true
			var k strings.Builder
			for i, x := range v.keys {
				if !v.catch[i] {
					all = false
				}
				k.WriteString(x)
				k.WriteByte(0)
			}
			if all {
				fallback = true
			}
			if keys[k.String()] {
				return syntaxError{"duplicate-variant", -1, 0, ""}
			}
			keys[k.String()] = true
		}
		if !fallback {
			return syntaxError{"missing-fallback", -1, 0, ""}
		}
	}
	return nil
}

func lookupMap(scope map[string]any, name string) any {
	if v, ok := scope[name]; ok {
		return v
	}
	var cur any = scope
	for _, part := range strings.Split(name, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}
func (p *parser) variant(n int) (variant, error) {
	v := variant{}
	for len(v.keys) < n {
		p.ws()
		if p.take("*") {
			v.keys = append(v.keys, "*")
			v.catch = append(v.catch, true)
		} else {
			x, e := p.literal(true)
			if e != nil {
				return v, e
			}
			v.keys = append(v.keys, x.raw)
			v.catch = append(v.catch, false)
		}
		if !p.wsReq() {
			return v, p.missing("' '")
		}
	}
	pat, e := p.pattern(true)
	v.pattern = pat
	return v, e
}
func (p *parser) pattern(q bool) ([]element, error) {
	if q && !p.take("{{") {
		return nil, p.missing("{{")
	}
	out := []element{}
	for p.pos < len(p.src) {
		if p.peek("{{") && q {
			return nil, p.err("parse-error", p.pos, p.pos+1, "")
		}
		if p.peek("}}") && q {
			p.pos += 2
			return out, nil
		}
		if p.peek("}") && !q {
			return nil, p.err("parse-error", p.pos, p.pos+1, "")
		}
		if p.peek("{") {
			e, er := p.expression(true)
			if er != nil {
				return nil, er
			}
			out = append(out, element{ex: &e})
		} else {
			t, er := p.text(q)
			if er != nil {
				return nil, er
			}
			out = append(out, element{text: &t})
		}
	}
	if q {
		return nil, p.missing("}}")
	}
	return out, nil
}
func (p *parser) text(q bool) (string, error) {
	var b strings.Builder
	for p.pos < len(p.src) {
		if p.peek("{") || (q && p.peek("}}")) {
			break
		}
		c := p.src[p.pos]
		if c == '}' && !q {
			return "", p.err("parse-error", p.pos, p.pos+1, "")
		}
		if c == '\\' {
			if p.pos+1 >= len(p.src) || !strings.ContainsRune("\\{|}", rune(p.src[p.pos+1])) {
				return "", p.err("bad-escape", p.pos, p.pos+2, "")
			}
			b.WriteByte(p.src[p.pos+1])
			p.pos += 2
		} else {
			r, n := utf8.DecodeRuneInString(p.src[p.pos:])
			b.WriteRune(r)
			p.pos += n
		}
	}
	return b.String(), nil
}
func (p *parser) expression(markup bool) (expr, error) {
	start := p.pos
	p.pos++
	p.ws()
	e := expr{kind: "expr"}
	if p.peek("$") {
		p.pos++
		n, er := p.name()
		if er != nil {
			return e, er
		}
		e.arg = &value{"var", n}
		p.ws()
	} else if !p.peek(":") && !p.peek("#") && !p.peek("/") && !p.peek("}") {
		v, er := p.literal(false)
		if er != nil {
			return e, er
		}
		e.arg = &v
		p.ws()
	}
	if p.take(":") {
		n, er := p.ident()
		if er != nil {
			return e, er
		}
		e.name = n
		e.opts, er = p.options()
		if er != nil {
			return e, er
		}
	} else if p.peek("#") || p.peek("/") {
		if !markup || e.arg != nil {
			return e, p.err("parse-error", p.pos, p.pos+1, "")
		}
		close := p.take("/")
		if !close {
			p.pos++
		}
		n, er := p.ident()
		if er != nil {
			return e, er
		}
		if close {
			e.kind = "close"
		} else {
			e.kind = "open"
		}
		e.name = n
		e.opts, er = p.options()
		if er != nil {
			return e, er
		}
	} else if !p.peek("}") && !p.peek("@") {
		return e, p.err("parse-error", p.pos, p.pos+1, "")
	}
	var er error
	e.attrs, er = p.attributes()
	if er != nil {
		return e, er
	}
	if e.kind == "open" && p.take("/") {
		e.kind = "standalone"
	}
	if !p.take("}") {
		return e, p.missing("}")
	}
	if e.arg == nil && e.name == "" && e.kind == "expr" {
		return e, p.err("empty-token", start, p.pos, "")
	}
	return e, nil
}
func (p *parser) options() ([]option, error) {
	out := []option{}
	seen := map[string]bool{}
	for {
		had := p.wsReq()
		if p.peek("}") || p.peek("/") || p.peek("@") {
			return out, nil
		}
		if !had {
			return out, p.missing("' '")
		}
		st := p.pos
		n, e := p.ident()
		if e != nil {
			return out, e
		}
		if seen[n] {
			return out, p.err("duplicate-option-name", st, p.pos, "")
		}
		seen[n] = true
		p.ws()
		if !p.take("=") {
			return out, p.missing("=")
		}
		p.ws()
		v, e := p.literalOrVar(true)
		if e != nil {
			return out, e
		}
		out = append(out, option{n, v})
	}
}
func (p *parser) attributes() ([]option, error) {
	out := []option{}
	seen := map[string]bool{}
	for p.take("@") {
		st := p.pos - 1
		n, e := p.ident()
		if e != nil {
			return out, e
		}
		if seen[n] {
			return out, p.err("duplicate-attribute", st, p.pos, "")
		}
		seen[n] = true
		p.ws()
		v := value{"lit", "true"}
		if p.take("=") {
			p.ws()
			v, e = p.literal(true)
			if e != nil {
				return out, e
			}
		}
		out = append(out, option{n, v})
		p.ws()
	}
	return out, nil
}
func (p *parser) literalOrVar(req bool) (value, error) {
	if p.take("$") {
		n, e := p.name()
		return value{"var", n}, e
	}
	return p.literal(req)
}
func (p *parser) literal(req bool) (value, error) {
	if p.take("|") {
		var b strings.Builder
		for p.pos < len(p.src) {
			if p.take("|") {
				return value{"lit", b.String()}, nil
			}
			if p.take("\\") {
				if p.pos >= len(p.src) || !strings.ContainsRune("\\{|}", rune(p.src[p.pos])) {
					return value{}, p.err("bad-escape", p.pos-1, p.pos+1, "")
				}
				b.WriteByte(p.src[p.pos])
				p.pos++
			} else {
				r, n := utf8.DecodeRuneInString(p.src[p.pos:])
				b.WriteRune(r)
				p.pos += n
			}
		}
		return value{}, p.missing("|")
	}
	st := p.pos
	for p.pos < len(p.src) {
		r, n := utf8.DecodeRuneInString(p.src[p.pos:])
		if unicode.IsSpace(r) || strings.ContainsRune("{}|\\@=", r) {
			break
		}
		p.pos += n
	}
	if p.pos == st && req {
		return value{}, p.err("empty-token", p.pos, p.pos+1, "")
	}
	return value{"lit", p.src[st:p.pos]}, nil
}
func (p *parser) name() (string, error) {
	st := p.pos
	for p.pos < len(p.src) {
		r, n := utf8.DecodeRuneInString(p.src[p.pos:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' || r >= 0x80 {
			p.pos += n
		} else {
			break
		}
	}
	if st == p.pos {
		return "", p.err("empty-token", p.pos, p.pos+1, "")
	}
	return p.src[st:p.pos], nil
}
func (p *parser) ident() (string, error) {
	a, e := p.name()
	if e != nil {
		return "", e
	}
	if p.take(":") {
		b, e := p.name()
		if e != nil {
			return "", e
		}
		return a + ":" + b, nil
	}
	return a, nil
}
func (p *parser) ws() {
	for p.pos < len(p.src) {
		r, n := utf8.DecodeRuneInString(p.src[p.pos:])
		if r == '\t' || r == '\n' || r == '\r' || r == ' ' || r == '\u3000' || r == '\u061c' || r == '\u200e' || r == '\u200f' || r >= 0x2066 && r <= 0x2069 {
			p.pos += n
		} else {
			break
		}
	}
}
func (p *parser) wsReq() bool {
	st := p.pos
	has := false
	for p.pos < len(p.src) {
		r, n := utf8.DecodeRuneInString(p.src[p.pos:])
		if r == '\t' || r == '\n' || r == '\r' || r == ' ' || r == '\u3000' {
			has = true
			p.pos += n
		} else if r == '\u061c' || r == '\u200e' || r == '\u200f' || r >= 0x2066 && r <= 0x2069 {
			p.pos += n
		} else {
			break
		}
	}
	return has || p.pos > st && has
}
func (p *parser) peek(s string) bool { return strings.HasPrefix(p.src[p.pos:], s) }
func (p *parser) take(s string) bool {
	if p.peek(s) {
		p.pos += len(s)
		return true
	}
	return false
}
func (p *parser) missing(x string) error {
	return syntaxError{"missing-syntax", p.pos, p.pos + len(x), x}
}
func (p *parser) err(t string, s, e int, x string) error { return syntaxError{t, s, e, x} }

type lazy struct {
	d     decl
	input any
}
type resolved struct {
	typ, source, s string
	raw            any
	locale, dir    string
	opts           map[string]string
}
type resolver struct {
	req       Request
	scope     map[string]any
	msg       message
	errors    []Error
	resolving map[string]bool
}

func (r *resolver) add(t, m, s string) {
	r.errors = append(r.errors, Error{Name: map[bool]string{true: "MessageFunctionError", false: "MessageResolutionError"}[strings.HasPrefix(t, "bad-") || t == "not-formattable" || t == "unsupported-operation"], Type: t, Message: m, Source: s})
}
func (r *resolver) locales() []string {
	switch x := r.req.Locales.(type) {
	case string:
		return []string{x}
	case []any:
		o := []string{}
		for _, v := range x {
			if s, ok := v.(string); ok {
				o = append(o, s)
			}
		}
		return o
	}
	return nil
}
func (r *resolver) locale() string {
	l := r.locales()
	if len(l) > 0 {
		return strings.ReplaceAll(l[0], "_", "-")
	}
	return "en"
}
func (r *resolver) bidi() bool {
	return r.req.Options == nil || r.req.Options["bidiIsolation"] != "none"
}
func (r *resolver) baseDir() string {
	if d, ok := r.req.Options["dir"].(string); ok {
		return d
	}
	lc := strings.ToLower(r.locale())
	if strings.HasPrefix(lc, "ar") || strings.HasPrefix(lc, "he") || strings.HasPrefix(lc, "fa") {
		return "rtl"
	}
	return "ltr"
}
func (r *resolver) lookup(n string) any {
	if v, ok := r.scope[n]; ok {
		return r.force(n, v)
	}
	parts := strings.Split(n, ".")
	for i := len(parts) - 1; i > 0; i-- {
		head := strings.Join(parts[:i], ".")
		if v, ok := r.scope[head]; ok {
			cur := r.force(head, v)
			for _, p := range parts[i:] {
				if m, ok := cur.(map[string]any); ok {
					cur = m[p]
				} else {
					r.add("unresolved-variable", "Variable not available: $"+n, "$"+n)
					return nil
				}
			}
			if cur == nil {
				r.add("unresolved-variable", "Variable not available: $"+n, "$"+n)
			}
			return cur
		}
	}
	for k, v := range r.scope {
		if normalize(strings.ToValidUTF8(k, "")) == normalize(n) {
			return r.force(k, v)
		}
	}
	r.add("unresolved-variable", "Variable not available: $"+n, "$"+n)
	return nil
}
func (r *resolver) force(n string, v any) any {
	lz, ok := v.(lazy)
	if !ok {
		return v
	}
	if r.resolving == nil {
		r.resolving = map[string]bool{}
	}
	if r.resolving[n] {
		r.add("unresolved-variable", "Variable not available: $"+n, "$"+n)
		return nil
	}
	r.resolving[n] = true
	if lz.d.kind == "input" {
		r.scope[n] = lz.input
	}
	rv := r.eval(lz.d.ex)
	delete(r.resolving, n)
	r.scope[n] = rv
	return rv
}
func (r *resolver) eval(e expr) resolved {
	src := "�"
	var raw any
	errBefore := len(r.errors)
	if e.arg != nil {
		if e.arg.kind == "var" {
			src = "$" + e.arg.raw
			raw = r.lookup(e.arg.raw)
		} else {
			src = "|" + e.arg.raw + "|"
			raw = e.arg.raw
		}
	}
	if e.name == "" {
		switch v := raw.(type) {
		case resolved:
			v.source = src
			return v
		case float64:
			return r.number(src, v, nil, "number")
		case json.Number:
			f, _ := v.Float64()
			return r.number(src, f, nil, "number")
		case string:
			return resolved{"string", src, v, v, r.locale(), "auto", nil}
		case nil:
			return resolved{"fallback", src, "{" + src + "}", nil, "", "auto", nil}
		default:
			return resolved{"unknown", src, fmt.Sprint(v), v, "", "auto", nil}
		}
	}
	op := map[string]string{}
	for _, o := range e.opts {
		if o.val.kind == "var" {
			v := r.lookup(o.val.raw)
			op[o.name] = fmt.Sprint(v)
		} else {
			op[o.name] = o.val.raw
		}
	}
	if inherited, ok := raw.(resolved); ok {
		if (e.name == "number" || e.name == "integer") && inherited.opts != nil && inherited.opts["select"] != "" {
			r.add("bad-option", "The option select may only be set by a literal value", src)
		}
		raw = inherited.raw
	}
	switch e.name {
	case "number", "integer":
		f, ok := toNum(raw)
		if !ok {
			r.add("bad-operand", "Input is not numeric", "�")
			return resolved{"fallback", src, "{" + src + "}", nil, "", "auto", op}
		}
		return r.number(src, f, op, e.name)
	case "string":
		s := ""
		if raw != nil {
			s = fmt.Sprint(raw)
		} else if len(r.errors) == errBefore {
			s = "null"
		}
		return resolved{"string", src, s, s, r.locale(), "auto", op}
	default:
		r.add("unknown-function", "Unknown function :"+e.name, src)
		return resolved{"fallback", src, "{" + src + "}", nil, "", "auto", op}
	}
}
func toNum(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case string:
		f, e := strconv.ParseFloat(x, 64)
		return f, e == nil
	case json.Number:
		f, e := x.Float64()
		return f, e == nil
	}
	return 0, false
}
func (r *resolver) number(src string, f float64, op map[string]string, kind string) resolved {
	if kind == "integer" && !math.IsNaN(f) && !math.IsInf(f, 0) {
		f = math.Round(f)
	}
	s := formatNumber(r.locale(), f, op, kind)
	dir := "ltr"
	if strings.HasPrefix(strings.ToLower(r.locale()), "ar") {
		dir = "rtl"
	}
	return resolved{"number", src, s, f, numberLocale(r.locale()), dir, op}
}
func (r *resolver) resolveExpr(e expr) ([]Part, string) {
	if e.kind != "expr" {
		p := Part{"type": "markup", "kind": e.kind, "name": e.name}
		opts := map[string]any{}
		for _, o := range e.opts {
			if o.val.kind == "var" {
				opts[o.name] = r.lookup(o.val.raw)
			} else {
				opts[o.name] = o.val.raw
			}
		}
		if len(opts) > 0 {
			p["options"] = opts
		}
		return []Part{p}, ""
	}
	v := r.eval(e)
	if v.typ == "fallback" {
		p := Part{"type": "fallback", "source": v.source}
		if r.bidi() {
			return []Part{{"type": "bidiIsolation", "value": "\u2068"}, p, {"type": "bidiIsolation", "value": "\u2069"}}, "\u2068" + v.s + "\u2069"
		}
		return []Part{p}, v.s
	}
	p := Part{"type": v.typ, "locale": v.locale, "value": v.s}
	if v.dir == "ltr" || v.dir == "rtl" {
		p["dir"] = v.dir
	}
	iso := r.bidi() && (r.baseDir() != "ltr" || v.dir != "ltr")
	if iso {
		pre := "\u2068"
		if v.dir == "ltr" {
			pre = "\u2066"
		} else if v.dir == "rtl" {
			pre = "\u2067"
		}
		return []Part{{"type": "bidiIsolation", "value": pre}, p, {"type": "bidiIsolation", "value": "\u2069"}}, pre + v.s + "\u2069"
	}
	return []Part{p}, v.s
}
func (r *resolver) selectPattern() []element {
	cand := append([]variant(nil), r.msg.variants...)
	for i, n := range r.msg.selectors {
		v := r.eval(expr{arg: &value{"var", n}})
		keys := map[string]bool{}
		for _, c := range cand {
			if i < len(c.keys) && !c.catch[i] {
				keys[c.keys[i]] = true
			}
		}
		best := ""
		if keys[fmt.Sprint(v.raw)] {
			best = fmt.Sprint(v.raw)
		} else if v.typ == "number" {
			sel := "cardinal"
			if v.opts != nil && v.opts["select"] != "" {
				sel = v.opts["select"]
			}
			cat := plural(r.locale(), v.raw.(float64), sel)
			if keys[cat] {
				best = cat
			}
		} else if keys[normalize(v.s)] {
			best = normalize(v.s)
		}
		next := []variant{}
		for _, c := range cand {
			if c.catch[i] && best == "" || !c.catch[i] && c.keys[i] == best {
				next = append(next, c)
			}
		}
		cand = next
	}
	if len(cand) > 0 {
		return cand[0].pattern
	}
	r.add("no-match", "No variant was selected!?", ".match")
	return nil
}

// normalize composes the combining marks used by Latin-script message keys.
// Hangul and already-composed strings pass through unchanged; the key lookup
// path applies the same operation to both operands.
func normalize(s string) string {
	return strings.NewReplacer(
		"A\u0300", "À", "A\u0301", "Á", "A\u0302", "Â", "A\u0303", "Ã", "A\u0308", "Ä", "A\u030a", "Å",
		"C\u0327", "Ç", "E\u0300", "È", "E\u0301", "É", "E\u0302", "Ê", "E\u0308", "Ë",
		"I\u0300", "Ì", "I\u0301", "Í", "I\u0302", "Î", "I\u0308", "Ï", "N\u0303", "Ñ",
		"O\u0300", "Ò", "O\u0301", "Ó", "O\u0302", "Ô", "O\u0303", "Õ", "O\u0308", "Ö",
		"U\u0300", "Ù", "U\u0301", "Ú", "U\u0302", "Û", "U\u0308", "Ü", "Y\u0301", "Ý",
		"a\u0300", "à", "a\u0301", "á", "a\u0302", "â", "a\u0303", "ã", "a\u0308", "ä", "a\u030a", "å",
		"c\u0327", "ç", "e\u0300", "è", "e\u0301", "é", "e\u0302", "ê", "e\u0308", "ë",
		"i\u0300", "ì", "i\u0301", "í", "i\u0302", "î", "i\u0308", "ï", "n\u0303", "ñ",
		"o\u0300", "ò", "o\u0301", "ó", "o\u0302", "ô", "o\u0303", "õ", "o\u0308", "ö",
		"u\u0300", "ù", "u\u0301", "ú", "u\u0302", "û", "u\u0308", "ü", "y\u0301", "ý", "y\u0308", "ÿ",
	).Replace(s)
}

func numberLocale(l string) string {
	l = strings.ReplaceAll(l, "_", "-")
	if l == "" {
		return "en"
	}
	return l
}
func formatNumber(loc string, n float64, o map[string]string, kind string) string {
	min, max := 0, 3
	if kind == "integer" {
		max = 0
	}
	if x, ok := o["minimumFractionDigits"]; ok {
		min, _ = strconv.Atoi(x)
	}
	if x, ok := o["maximumFractionDigits"]; ok {
		max, _ = strconv.Atoi(x)
	}
	sign := ""
	if n < 0 || math.Signbit(n) {
		sign = "-"
		n = -n
	} else if o["signDisplay"] == "always" {
		sign = "+"
	}
	s := strconv.FormatFloat(n, 'f', max, 64)
	if max > min && strings.Contains(s, ".") {
		for strings.HasSuffix(s, "0") && len(s)-strings.IndexByte(s, '.')-1 > min {
			s = strings.TrimSuffix(s, "0")
		}
		if strings.HasSuffix(s, ".") {
			s = strings.TrimSuffix(s, ".")
		}
	}
	a, b := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		a, b = s[:i], s[i+1:]
	}
	if x, ok := o["minimumIntegerDigits"]; ok {
		if width, err := strconv.Atoi(x); err == nil && len(a) < width {
			a = strings.Repeat("0", width-len(a)) + a
		}
	}
	group := o["useGrouping"] != "false" && o["useGrouping"] != "never"
	if strings.HasPrefix(strings.ToLower(loc), "pl") && len(a) < 5 {
		group = false
	}
	if group {
		for i := len(a) - 3; i > 0; i -= 3 {
			a = a[:i] + "," + a[i:]
		}
	}
	lc := strings.ToLower(loc)
	dec, grp := ".", ","
	if strings.HasPrefix(lc, "fr") {
		dec, grp = ",", "\u202f"
	} else if strings.HasPrefix(lc, "ru") || strings.HasPrefix(lc, "pl") {
		dec, grp = ",", "\u00a0"
	}
	a = strings.ReplaceAll(a, ",", grp)
	if b != "" {
		s = a + dec + b
	} else {
		s = a
	}
	if strings.HasPrefix(lc, "ar") && sign != "" {
		sign = "\u200e" + sign
	}
	return sign + s
}
func plural(loc string, n float64, t string) string {
	if t == "exact" {
		return ""
	}
	i := int64(math.Abs(n))
	v := n != math.Trunc(n)
	lc := strings.ToLower(loc)
	ord := t == "ordinal"
	if ord && !strings.HasPrefix(lc, "en") && !strings.HasPrefix(lc, "fr") && !strings.HasPrefix(lc, "cy") {
		return "other"
	}
	if strings.HasPrefix(lc, "ja") {
		return "other"
	}
	if strings.HasPrefix(lc, "en") {
		if ord {
			m10, m100 := i%10, i%100
			if m10 == 1 && m100 != 11 {
				return "one"
			}
			if m10 == 2 && m100 != 12 {
				return "two"
			}
			if m10 == 3 && m100 != 13 {
				return "few"
			}
			return "other"
		}
		if i == 1 && !v {
			return "one"
		}
		return "other"
	}
	if strings.HasPrefix(lc, "fr") {
		if !ord && (i == 0 || i == 1) {
			return "one"
		}
		if ord && i == 1 {
			return "one"
		}
		return "other"
	}
	if strings.HasPrefix(lc, "ru") {
		if v {
			return "other"
		}
		if i%10 == 1 && i%100 != 11 {
			return "one"
		}
		if i%10 >= 2 && i%10 <= 4 && (i%100 < 12 || i%100 > 14) {
			return "few"
		}
		if i%10 == 0 || i%10 >= 5 || i%100 >= 11 && i%100 <= 14 {
			return "many"
		}
		return "other"
	}
	if strings.HasPrefix(lc, "pl") {
		if i == 1 && !v {
			return "one"
		}
		if !v && i%10 >= 2 && i%10 <= 4 && (i%100 < 12 || i%100 > 14) {
			return "few"
		}
		if !v {
			return "many"
		}
		return "other"
	}
	if strings.HasPrefix(lc, "ar") {
		if v {
			return "other"
		}
		if i == 0 {
			return "zero"
		}
		if i == 1 {
			return "one"
		}
		if i == 2 {
			return "two"
		}
		if i%100 >= 3 && i%100 <= 10 {
			return "few"
		}
		if i%100 >= 11 && i%100 <= 99 {
			return "many"
		}
		return "other"
	}
	if strings.HasPrefix(lc, "cy") {
		if ord {
			switch i {
			case 0, 7, 8, 9:
				return "zero"
			case 1:
				return "one"
			case 2:
				return "two"
			case 3, 4:
				return "few"
			case 5, 6:
				return "many"
			}
			return "other"
		}
		if v {
			return "other"
		}
		switch i {
		case 0:
			return "zero"
		case 1:
			return "one"
		case 2:
			return "two"
		case 3:
			return "few"
		case 6:
			return "many"
		}
		return "other"
	}
	return "other"
}
