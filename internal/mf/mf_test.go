// SPDX-License-Identifier: Apache-2.0
// Ported from the MessageFormat 2 package; see mf2/messageformat/LICENSE.
package mf

import "testing"

func TestFormatSelectionAndLocale(t *testing.T) {
	r := Format(Request{Locales: "ru", Source: ".input {$n :number select=cardinal}\n.match $n\none {{one}}\nfew {{few {$n}}}\nmany {{many {$n}}}\n* {{other}}", Params: map[string]any{"n": 22.0}, Options: map[string]any{"bidiIsolation": "none"}})
	if r.Status != 0 || r.Output == nil || *r.Output != "few 22" {
		t.Fatalf("unexpected result: %#v", r)
	}
}

func TestFallbackAndError(t *testing.T) {
	r := Format(Request{Locales: "en", Source: "x {$missing} y", Options: map[string]any{"bidiIsolation": "none"}})
	if r.Output == nil || *r.Output != "x {$missing} y" {
		t.Fatalf("unexpected output: %#v", r.Output)
	}
	if len(r.Errors) != 1 || r.Errors[0].Type != "unresolved-variable" {
		t.Fatalf("unexpected errors: %#v", r.Errors)
	}
}

func TestSyntaxError(t *testing.T) {
	r := Format(Request{Locales: "en", Source: "{$x :number a=1 a=2}"})
	if r.Status != 2 || len(r.Errors) != 1 || r.Errors[0].Type != "duplicate-option-name" {
		t.Fatalf("unexpected response: %#v", r)
	}
}

func TestMarkupParts(t *testing.T) {
	r := Format(Request{Locales: "en", Source: "{#b tone=|warm|}x{/b}", Options: map[string]any{"bidiIsolation": "none"}})
	parts := r.Parts.([]Part)
	if len(parts) != 3 || parts[0]["type"] != "markup" || parts[2]["kind"] != "close" {
		t.Fatalf("unexpected parts: %#v", parts)
	}
}

func TestBidiIsolation(t *testing.T) {
	r := Format(Request{Locales: "ar", Source: "x {$n :number} y", Params: map[string]any{"n": 42.0}})
	if r.Output == nil || *r.Output != "x \u206742\u2069 y" {
		t.Fatalf("unexpected bidi output: %#v", r)
	}
	parts := r.Parts.([]Part)
	if len(parts) != 5 || parts[1]["value"] != "\u2067" || parts[3]["value"] != "\u2069" {
		t.Fatalf("unexpected bidi parts: %#v", parts)
	}
}
