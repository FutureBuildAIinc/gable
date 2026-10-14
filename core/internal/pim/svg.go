// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pim

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ErrUnsafeSVG is what SanitizeSVG answers for a drawing it will not keep:
// every refusal matches it (errors.Is) and carries its own plain reason.
var ErrUnsafeSVG = errors.New("the illustration was refused")

// The limits a model drawn illustration must meet.
const (
	maxSVGBytes      = 64 << 10
	maxSVGElements   = 500
	maxSVGDepth      = 24
	maxSVGValueBytes = 20 << 10
	maxLabelBytes    = 32
	maxLabelsBytes   = 128
	svgNamespace     = "http://www.w3.org/2000/svg"
	xlinkNamespace   = "http://www.w3.org/1999/xlink"
)

// svgElements is the allowlist of elements that are kept. Drawing elements
// carry the picture; text and tspan only carry a short label.
var svgElements = map[string]bool{
	"svg": true, "g": true, "path": true, "rect": true, "circle": true, "ellipse": true,
	"line": true, "polyline": true, "polygon": true, "defs": true, "linearGradient": true,
	"radialGradient": true, "stop": true, "clipPath": true, "text": true, "tspan": true,
}

// drawing elements: at least one must survive, or nothing was drawn.
var svgDrawing = map[string]bool{
	"path": true, "rect": true, "circle": true, "ellipse": true, "line": true,
	"polyline": true, "polygon": true, "text": true,
}

// svgAttributes is the allowlist of attributes that are kept: geometry,
// presentation, gradient and id. href is handled apart (same document only).
var svgAttributes = map[string]bool{
	// geometry
	"x": true, "y": true, "width": true, "height": true, "cx": true, "cy": true, "r": true,
	"rx": true, "ry": true, "x1": true, "y1": true, "x2": true, "y2": true, "fx": true, "fy": true,
	"points": true, "d": true, "transform": true, "preserveAspectRatio": true,
	"clipPathUnits": true,
	// presentation
	"fill": true, "fill-opacity": true, "fill-rule": true, "clip-rule": true, "clip-path": true,
	"stroke": true, "stroke-width": true, "stroke-linecap": true, "stroke-linejoin": true,
	"stroke-miterlimit": true, "stroke-dasharray": true, "stroke-dashoffset": true,
	"stroke-opacity": true, "opacity": true, "font-family": true, "font-size": true,
	"font-weight": true, "text-anchor": true,
	// gradient
	"offset": true, "stop-color": true, "stop-opacity": true, "gradientUnits": true,
	"gradientTransform": true, "spreadMethod": true,
	// identity
	"id": true,
}

// attackElements and attackAttributes are refused, not dropped: a drawing
// that carries one is not kept at all.
var attackElements = map[string]bool{
	"script": true, "foreignObject": true, "image": true, "use": true, "style": true,
	"animate": true, "animateTransform": true, "animateMotion": true, "animateColor": true,
	"set": true, "iframe": true, "object": true, "embed": true, "a": true, "link": true,
	"handler": true, "listener": true, "audio": true, "video": true, "canvas": true,
}

var (
	idRE       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	sameDocRE  = regexp.MustCompile(`^#[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	urlOpenRE  = regexp.MustCompile(`(?i)url\s*\(`)
	urlLocalRE = regexp.MustCompile(`(?i)url\(\s*#[A-Za-z][A-Za-z0-9_-]{0,63}\s*\)`)
	schemeRE   = regexp.MustCompile(`(?i)\b(javascript|data|vbscript|file|https?|ftp)\s*:`)
)

func unsafe(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsafeSVG, fmt.Sprintf(format, args...))
}

// SanitizeSVG parses model drawn SVG as XML and re-serialises only what the
// allowlists keep. It never passes the model's text through. A drawing that
// carries a DOCTYPE or entity, a script, an event attribute, a style, an
// external reference, an embedded raster or any other attack, or that is
// oversized, not 256 square, or empty, is refused with ErrUnsafeSVG. A part
// outside the allowlists that is not itself an attack is dropped.
func SanitizeSVG(in []byte) ([]byte, error) {
	if len(in) == 0 {
		return nil, unsafe("the drawing is empty")
	}
	if len(in) > maxSVGBytes {
		return nil, unsafe("the drawing is larger than %d KB", maxSVGBytes>>10)
	}
	dec := xml.NewDecoder(bytes.NewReader(in))
	dec.Strict = true
	dec.Entity = nil // no entity beyond the five XML ones

	var (
		out      bytes.Buffer
		stack    []string // kept element names, for closing tags
		skip     int      // depth of a dropped subtree being skipped
		elements int
		drawn    bool
		rooted   bool
		labels   int
		inText   int
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, unsafe("the drawing is not well formed XML")
		}
		switch t := tok.(type) {
		case xml.Directive:
			return nil, unsafe("a DOCTYPE or entity is not allowed")
		case xml.ProcInst:
			if t.Target != "xml" {
				return nil, unsafe("a processing instruction is not allowed")
			}
		case xml.Comment:
			// dropped
		case xml.StartElement:
			if rooted && len(stack) == 0 && skip == 0 {
				return nil, unsafe("more than one root element")
			}
			if skip > 0 {
				if err := checkDroppedStart(t); err != nil {
					return nil, err
				}
				skip++
				continue
			}
			elements++
			if elements > maxSVGElements {
				return nil, unsafe("the drawing has more than %d elements", maxSVGElements)
			}
			if len(stack)+1 > maxSVGDepth {
				return nil, unsafe("the drawing is nested more than %d deep", maxSVGDepth)
			}
			name := t.Name.Local
			if t.Name.Space != "" && t.Name.Space != svgNamespace {
				return nil, unsafe("an element from another namespace is not allowed")
			}
			if attackElements[name] || strings.HasPrefix(name, "animate") {
				return nil, unsafe("the %s element is not allowed", name)
			}
			if !rooted {
				if name != "svg" {
					return nil, unsafe("the drawing is not an svg")
				}
				rooted = true
			} else if name == "svg" {
				skip = 1 // a nested svg is dropped with its subtree
				continue
			}
			if !svgElements[name] {
				if err := checkDroppedStart(t); err != nil {
					return nil, err
				}
				skip = 1
				continue
			}
			attrs, err := keptAttributes(name, t.Attr, len(stack) == 0)
			if err != nil {
				return nil, err
			}
			if svgDrawing[name] {
				drawn = true
			}
			if name == "text" || name == "tspan" {
				inText++
			}
			out.WriteString("<" + name)
			if name == "svg" {
				out.WriteString(` xmlns="` + svgNamespace + `" width="256" height="256" viewBox="0 0 256 256"`)
			}
			for _, a := range attrs {
				out.WriteString(" " + a.name + `="`)
				if err := xml.EscapeText(&out, []byte(a.value)); err != nil {
					return nil, unsafe("the drawing cannot be written")
				}
				out.WriteString(`"`)
			}
			out.WriteString(">")
			stack = append(stack, name)
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			if len(stack) == 0 {
				return nil, unsafe("the drawing is not well formed")
			}
			name := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if name == "text" || name == "tspan" {
				inText--
			}
			out.WriteString("</" + name + ">")
		case xml.CharData:
			if skip > 0 {
				continue
			}
			s := strings.TrimSpace(string(t))
			if s == "" {
				continue
			}
			if inText == 0 {
				continue // stray text outside a label is dropped
			}
			s = strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return -1
				}
				return r
			}, s)
			if len(s) > maxLabelBytes {
				return nil, unsafe("a label is longer than %d characters", maxLabelBytes)
			}
			labels += len(s)
			if labels > maxLabelsBytes {
				return nil, unsafe("the labels are too long")
			}
			if err := xml.EscapeText(&out, []byte(s)); err != nil {
				return nil, unsafe("the drawing cannot be written")
			}
		}
	}
	if !rooted || len(stack) != 0 {
		return nil, unsafe("the drawing is not a complete svg")
	}
	if !drawn {
		return nil, unsafe("the drawing has nothing drawn in it")
	}
	return out.Bytes(), nil
}

// checkDroppedStart looks inside an element that is dropped: an attack on it
// still refuses the whole drawing.
func checkDroppedStart(t xml.StartElement) error {
	name := t.Name.Local
	if t.Name.Space != "" && t.Name.Space != svgNamespace {
		return unsafe("an element from another namespace is not allowed")
	}
	if attackElements[name] || strings.HasPrefix(name, "animate") {
		return unsafe("the %s element is not allowed", name)
	}
	for _, a := range t.Attr {
		if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
			continue
		}
		if err := checkAttackAttr(a); err != nil {
			return err
		}
	}
	return nil
}

// checkAttackAttr refuses the attributes that are attacks wherever they sit.
func checkAttackAttr(a xml.Attr) error {
	local := a.Name.Local
	lower := strings.ToLower(local)
	switch {
	case strings.HasPrefix(lower, "on"):
		return unsafe("the %s attribute is not allowed", local)
	case lower == "style":
		return unsafe("a style is not allowed")
	}
	if err := checkValue(a.Value); err != nil {
		return err
	}
	if lower == "href" {
		if !sameDocRE.MatchString(strings.TrimSpace(a.Value)) {
			return unsafe("a reference outside the drawing is not allowed")
		}
	}
	return nil
}

// checkValue refuses a value that reaches outside the drawing: a scheme, or
// a url( that is not a same document #id.
func checkValue(v string) error {
	if len(v) > maxSVGValueBytes {
		return unsafe("an attribute value is too long")
	}
	if schemeRE.MatchString(v) {
		return unsafe("a URL or data value is not allowed")
	}
	if n := len(urlOpenRE.FindAllString(v, -1)); n > 0 {
		if len(urlLocalRE.FindAllString(v, -1)) != n {
			return unsafe("a url() that is not a same document #id is not allowed")
		}
	}
	lower := strings.ToLower(v)
	if strings.Contains(lower, "expression(") || strings.Contains(lower, "@import") {
		return unsafe("a script like value is not allowed")
	}
	return nil
}

type keptAttr struct{ name, value string }

// keptAttributes applies the attribute allowlist to one element. The root's
// viewBox must be 256 square; the root's own size is replaced by ours.
func keptAttributes(element string, attrs []xml.Attr, root bool) ([]keptAttr, error) {
	var kept []keptAttr
	sawViewBox := false
	for _, a := range attrs {
		local := a.Name.Local
		space := a.Name.Space
		if space == "xmlns" || local == "xmlns" {
			continue // a namespace declaration; the output declares its own
		}
		if err := checkAttackAttr(a); err != nil {
			return nil, err
		}
		if local == "href" && (space == "" || space == xlinkNamespace) {
			if element == "linearGradient" || element == "radialGradient" {
				kept = append(kept, keptAttr{"href", strings.TrimSpace(a.Value)})
			}
			continue
		}
		if space != "" {
			continue // another namespace's attribute (editor metadata) is dropped
		}
		if root && element == "svg" {
			if local == "viewBox" {
				if err := checkViewBox(a.Value); err != nil {
					return nil, err
				}
				sawViewBox = true
			}
			continue // the root's attributes are ours alone
		}
		if !svgAttributes[local] {
			continue
		}
		if local == "id" && !idRE.MatchString(a.Value) {
			return nil, unsafe("an id is not a plain name")
		}
		kept = append(kept, keptAttr{local, a.Value})
	}
	if root && element == "svg" && !sawViewBox {
		return nil, unsafe("the drawing has no viewBox of 0 0 256 256")
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].name < kept[j].name })
	return kept, nil
}

func checkViewBox(v string) error {
	f := strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\n' })
	if len(f) != 4 {
		return unsafe("the viewBox is not 0 0 256 256")
	}
	want := [4]float64{0, 0, 256, 256}
	for i, s := range f {
		n, err := strconv.ParseFloat(s, 64)
		if err != nil || n != want[i] {
			return unsafe("the viewBox is not 0 0 256 256")
		}
	}
	return nil
}
