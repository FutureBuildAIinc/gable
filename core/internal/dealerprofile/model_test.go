// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dealerprofile

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"testing"
)

// restartJPEG is a 24 by 16 baseline JPEG with a restart interval (a DRI and
// an RST marker inside its scan), which Go's encoder cannot write.
const restartJPEG = "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAYEBQYFBAYGBQYHBwYIChAKCgkJChQODwwQFxQYGBcUFhYaHSUfGhsjHBYWICwgIyYnKSopGR8tMC0oMCUoKSj/2wBDAQcHBwoIChMKChMoGhYaKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCj/wAARCAAQABgDASIAAhEBAxEB/8QAHwAAAQUBAQEBAQEAAAAAAAAAAAECAwQFBgcICQoL/8QAtRAAAgEDAwIEAwUFBAQAAAF9AQIDAAQRBRIhMUEGE1FhByJxFDKBkaEII0KxwRVS0fAkM2JyggkKFhcYGRolJicoKSo0NTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqDhIWGh4iJipKTlJWWl5iZmqKjpKWmp6ipqrKztLW2t7i5usLDxMXGx8jJytLT1NXW19jZ2uHi4+Tl5ufo6erx8vP09fb3+Pn6/8QAHwEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoL/8QAtREAAgECBAQDBAcFBAQAAQJ3AAECAxEEBSExBhJBUQdhcRMiMoEIFEKRobHBCSMzUvAVYnLRChYkNOEl8RcYGRomJygpKjU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6goOEhYaHiImKkpOUlZaXmJmaoqOkpaanqKmqsrO0tba3uLm6wsPExcbHyMnK0tPU1dbX2Nna4uPk5ebn6Onq8vP09fb3+Pn6/90ABAAB/9oADAMBAAIRAxEAPwDx+x8P9Pk/SugsvD/T5P0rvrHw/wBPk/SugsfD/T5P0r0MTnXmeRkvEO2p/9DnrHw/0+T9KK9fsfD/AE+T9KK5amde9ufcYPiH92tT/9k="

// TestJPEGEndWalk pins each clause of the segment walk: a restart marker and
// fill bytes inside the scan are not the end, and every truncated or hostile
// structure is refused without a panic.
func TestJPEGEndWalk(t *testing.T) {
	rst, err := base64.StdEncoding.DecodeString(restartJPEG)
	if err != nil {
		t.Fatal(err)
	}
	var plainBuf bytes.Buffer
	if err := jpeg.Encode(&plainBuf, image.NewRGBA(image.Rect(0, 0, 16, 16)), nil); err != nil {
		t.Fatal(err)
	}
	plain := plainBuf.Bytes()
	withFill := append(append([]byte{}, plain[:len(plain)-2]...), 0xff, 0xff, 0xff, 0xd9)

	cases := []struct {
		name   string
		raw    []byte
		wantOK bool
		wantAt int
	}{
		{"plain", plain, true, len(plain)},
		{"restart markers", rst, true, len(rst)},
		{"fill bytes before the end", withFill, true, len(withFill)},
		{"scan with no end marker", plain[:len(plain)-2], false, 0},
		{"trailing lone FF", append(append([]byte{}, plain[:len(plain)-2]...), 0xff), false, 0},
		{"truncated mid header", plain[:5], false, 0},
		{"segment length runs off the end", []byte{0xff, 0xd8, 0xff, 0xe0, 0xff, 0xff, 0x01, 0x02}, false, 0},
		{"segment length zero", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x00, 0xff, 0xd9}, false, 0},
		{"segment length one", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x01, 0xff, 0xd9}, false, 0},
		{"marker code zero", []byte{0xff, 0xd8, 0xff, 0x00, 0xff, 0xd9}, false, 0},
		{"marker code zero with a length", []byte{0xff, 0xd8, 0xff, 0x00, 0x00, 0x02, 0xff, 0xd9}, false, 0},
		{"scan header length one", []byte{0xff, 0xd8, 0xff, 0xda, 0x00, 0x01, 0xff, 0xd9}, false, 0},
		{"length cut at the buffer", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00}, false, 0},
		{"only fill after the start", append([]byte{0xff, 0xd8}, bytes.Repeat([]byte{0xff}, 40)...), false, 0},
		{"junk where a marker belongs", []byte{0xff, 0xd8, 0x12, 0x34, 0xff, 0xd9}, false, 0},
		{"too short", []byte{0xff, 0xd8}, false, 0},
	}
	for _, c := range cases {
		end, ok := jpegEnd(c.raw)
		if ok != c.wantOK || end != c.wantAt {
			t.Errorf("%s: jpegEnd = %d, %v; want %d, %v", c.name, end, ok, c.wantAt, c.wantOK)
		}
	}
}

// TestNormalizeGSTHSTNumber pins the GST/HST number's shape (CP-14 A1): nine
// digits, RT, four digits, stored without the spaces and hyphens a person
// types, printed with one space before the RT. A payroll (RP) or short
// business number is refused; empty is allowed.
func TestNormalizeGSTHSTNumber(t *testing.T) {
	ok := []struct{ in, want string }{
		{"123456789RT0001", "123456789RT0001"},
		{"123456789 RT0001", "123456789RT0001"},
		{"123456789-RT-0001", "123456789RT0001"},
		{" 123 456 789 rt 0001 ", "123456789RT0001"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range ok {
		got, err := NormalizeGSTHSTNumber(c.in)
		if err != nil || got != c.want {
			t.Errorf("NormalizeGSTHSTNumber(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"12345678RT0001", "123456789RP0001", "1234567890RT0001", "123456789RT001", "123456789RT00011", "ABCDEFGHIRT0001", "123456789"} {
		if got, err := NormalizeGSTHSTNumber(in); err == nil {
			t.Errorf("NormalizeGSTHSTNumber(%q) = %q, want a refusal", in, got)
		}
	}
	if got := FormatGSTHSTNumber("123456789RT0001"); got != "123456789 RT0001" {
		t.Errorf("FormatGSTHSTNumber = %q", got)
	}
	if got := FormatGSTHSTNumber(""); got != "" {
		t.Errorf("FormatGSTHSTNumber(empty) = %q", got)
	}
}
