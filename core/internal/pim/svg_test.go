// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pim

import (
	"encoding/xml"
	"errors"
	"strings"
	"testing"
)

const cleanDrawing = `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 256 256" width="400" height="400">
  <!-- a stud -->
  <defs>
    <linearGradient id="wood" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="#d9b382"/>
      <stop offset="1" stop-color="#b98a52"/>
    </linearGradient>
    <clipPath id="clip"><rect x="0" y="0" width="256" height="256"/></clipPath>
  </defs>
  <g clip-path="url(#clip)">
    <rect x="20" y="100" width="216" height="56" rx="4" fill="url(#wood)" stroke="#7a5a30" stroke-width="2"/>
    <path d="M20 128 L236 128" stroke="#7a5a30" fill="none"/>
    <circle cx="60" cy="128" r="3" fill="#7a5a30"/>
    <text x="128" y="200" font-size="12" text-anchor="middle"><tspan>SPF 2x4</tspan></text>
  </g>
</svg>`

func TestSanitizeSVG_KeepsCleanDrawing(t *testing.T) {
	out, err := SanitizeSVG([]byte(cleanDrawing))
	if err != nil {
		t.Fatalf("clean drawing refused: %v", err)
	}
	s := string(out)
	for _, want := range []string{`viewBox="0 0 256 256"`, `<linearGradient`, `<stop`, `<clipPath`, `<rect`, `<path`, `<circle`, `fill="url(#wood)"`, `xmlns="http://www.w3.org/2000/svg"`} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
	for _, bad := range []string{"<!--", "<?xml", "xlink", `width="400"`} {
		if strings.Contains(s, bad) {
			t.Errorf("output keeps %q", bad)
		}
	}
	// What is kept is itself clean: a second pass is the identity.
	again, err := SanitizeSVG(out)
	if err != nil || string(again) != s {
		t.Errorf("sanitising twice changed the drawing (err %v)", err)
	}
}

func wrap(inner string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256">` + inner + `</svg>`
}

func TestSanitizeSVG_RefusesEachAttack(t *testing.T) {
	big := wrap(`<path d="` + strings.Repeat("M0 0 L1 1 ", 7000) + `"/>`)
	manyElements := wrap(strings.Repeat(`<rect width="1" height="1"/>`, 600))
	deep := wrap(strings.Repeat(`<g>`, 40) + `<rect width="1" height="1"/>` + strings.Repeat(`</g>`, 40))
	cases := []struct{ name, svg string }{
		{"script element", wrap(`<script>alert(1)</script><rect width="1" height="1"/>`)},
		{"script inside a group", wrap(`<g><script>alert(1)</script></g><rect width="1" height="1"/>`)},
		{"event attribute", wrap(`<rect width="1" height="1" onload="alert(1)"/>`)},
		{"event attribute on the root", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256" onclick="x()"><rect width="1" height="1"/></svg>`},
		{"external href on a gradient", wrap(`<defs><linearGradient id="a" href="https://evil.example/x.svg#a"/></defs><rect width="1" height="1"/>`)},
		{"external xlink href", `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 256 256"><defs><linearGradient id="a" xlink:href="http://evil.example/a"/></defs><rect width="1" height="1"/></svg>`},
		{"data uri value", wrap(`<rect width="1" height="1" fill="data:text/html;base64,PHNjcmlwdD4="/>`)},
		{"javascript value", wrap(`<defs><linearGradient id="a" href="javascript:alert(1)"/></defs><rect width="1" height="1"/>`)},
		{"foreignObject", wrap(`<foreignObject width="10" height="10"><div xmlns="http://www.w3.org/1999/xhtml">x</div></foreignObject><rect width="1" height="1"/>`)},
		{"doctype with entity", `<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY x "boom">]>` + wrap(`<rect width="1" height="1"/>`)},
		{"doctype without entity", `<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">` + wrap(`<rect width="1" height="1"/>`)},
		{"oversized file", big},
		{"too many elements", manyElements},
		{"too deep", deep},
		{"raster image element", wrap(`<image href="data:image/png;base64,AAAA" width="10" height="10"/><rect width="1" height="1"/>`)},
		{"style element", wrap(`<style>rect{fill:url(https://evil.example/x)}</style><rect width="1" height="1"/>`)},
		{"style attribute with url", wrap(`<rect width="1" height="1" style="fill:url(https://evil.example/x)"/>`)},
		{"url to another document", wrap(`<rect width="1" height="1" fill="url(https://evil.example/x.svg#a)"/>`)},
		{"use element", wrap(`<defs><rect id="r" width="1" height="1"/></defs><use href="#r"/>`)},
		{"animate element", wrap(`<rect width="1" height="1"><animate attributeName="fill" to="red"/></rect>`)},
		{"set element", wrap(`<rect width="1" height="1"><set attributeName="onclick" to="x()"/></rect>`)},
		{"wrong viewBox", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 400"><rect width="1" height="1"/></svg>`},
		{"no viewBox", `<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" height="1"/></svg>`},
		{"not svg at the root", `<html><body>hi</body></html>`},
		{"not xml", `this is not a drawing`},
		{"empty", ``},
		{"nothing drawn", wrap(`<g></g>`)},
		{"foreign namespace element", wrap(`<h:script xmlns:h="http://www.w3.org/1999/xhtml">x</h:script><rect width="1" height="1"/>`)},
		{"css escaped url", wrap(`<rect width="1" height="1" fill="u\72l(//evil.test/a.svg#x)"/>`)},
		{"css escaped url short form", wrap(`<rect width="1" height="1" fill="\75rl(//evil.test/a.svg#x)"/>`)},
		{"css escaped url as a character reference", wrap(`<rect width="1" height="1" fill="&#92;75rl(//evil.test/a.svg#x)"/>`)},
		{"url with a comment inside", wrap(`<rect width="1" height="1" fill="url/**/(//evil.test/a)"/>`)},
		{"output over the cap", wrap(`<rect width="1" height="1" fill="` + strings.Repeat("\n", 19000) + `" stroke="` + strings.Repeat("\n", 19000) + `" opacity="` + strings.Repeat("\n", 19000) + `"/>`)},
		{"escaped url in clip-path", wrap(`<rect width="1" height="1" clip-path="\75rl(//evil.example/x)"/>`)},
		{"paint that is not a colour or a reference", wrap(`<rect width="1" height="1" fill="//evil.example/x"/>`)},
		{"paint function that is not rgb", wrap(`<rect width="1" height="1" stroke="image(//evil.example/x)"/>`)},
		{"stop colour that is not a colour", wrap(`<defs><linearGradient id="a"><stop offset="0" stop-color="url(//evil.example/x)"/></linearGradient></defs><rect width="1" height="1"/>`)},
		{"clip-path that is not a reference", wrap(`<rect width="1" height="1" clip-path="circle(10px)"/>`)},
		{"quote growth over the cap", wrap(`<path d='` + strings.Repeat(`"`, 14000) + `' stroke='` + strings.Repeat(`"`, 14000) + `' fill='` + strings.Repeat(`"`, 14000) + `'/>`)},
		{"trailing second root", wrap(`<rect width="1" height="1"/>`) + wrap(`<rect width="1" height="1"/>`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := SanitizeSVG([]byte(tc.svg))
			if err == nil {
				t.Fatalf("attack kept; output:\n%.300s", out)
			}
			if !errors.Is(err, ErrUnsafeSVG) {
				t.Errorf("error %v does not match ErrUnsafeSVG", err)
			}
			if out != nil {
				t.Errorf("a refused drawing returned bytes")
			}
		})
	}
}

// An element or attribute outside the allowlists that is not itself an attack
// is dropped, and the rest of the drawing is kept.
func TestSanitizeSVG_DropsUnlistedHarmlessParts(t *testing.T) {
	out, err := SanitizeSVG([]byte(wrap(`<title>x</title><filter id="f"><feGaussianBlur stdDeviation="2"/></filter><rect width="4" height="4" filter="url(#f)" data-x="1" fill="#fff"/>`)))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, gone := range []string{"<title", "<filter", "feGaussianBlur", "data-x"} {
		if strings.Contains(s, gone) {
			t.Errorf("output keeps %q: %s", gone, s)
		}
	}
	if !strings.Contains(s, `<rect`) || !strings.Contains(s, `fill="#fff"`) {
		t.Errorf("clean part lost: %s", s)
	}
}

// A repeated attribute keeps its first value, so the output is well formed
// XML a browser parses, and a second pass is the identity.
func TestSanitizeSVG_KeepsFirstOfRepeatedAttribute(t *testing.T) {
	out, err := SanitizeSVG([]byte(wrap(`<rect x="1" x="2" width="4" height="4" fill="red" fill="blue"/>`)))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Count(s, ` x="`) != 1 || strings.Count(s, ` fill="`) != 1 || !strings.Contains(s, `x="1"`) || !strings.Contains(s, `fill="red"`) {
		t.Errorf("repeated attributes kept: %s", s)
	}
	if err := xml.Unmarshal(out, new(struct{ XMLName xml.Name })); err != nil {
		t.Errorf("output is not well formed: %v", err)
	}
}

// The paint and reference attributes are held to a small grammar; these are
// kept as they are, and every accepted output is its own fixed point.
func TestSanitizeSVG_PaintGrammarAndIdempotence(t *testing.T) {
	for _, paint := range []string{"none", "currentColor", "#fff", "#ffff", "#c9a36b", "#c9a36bcc", "red", "rgb(10,20,30)", "rgba(10, 20, 30, 0.5)", "rgb(10%,20%,30%)", "url(#wood)", "url( #wood )"} {
		in := wrap(`<defs><linearGradient id="wood"/></defs><rect width="4" height="4" fill="` + paint + `" stroke="` + paint + `"/>`)
		out, err := SanitizeSVG([]byte(in))
		if err != nil {
			t.Errorf("paint %q refused: %v", paint, err)
			continue
		}
		again, err := SanitizeSVG(out)
		if err != nil || string(again) != string(out) {
			t.Errorf("paint %q: second pass changed the drawing (err %v)", paint, err)
		}
	}
}
