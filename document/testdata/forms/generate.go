//go:build ignore

// generate writes acroform.pdf beside this file: a one-page PDF whose AcroForm holds filled
// fields of every kind the forms record reads, written by hand, every byte fixed, so a rerun
// writes the same bytes. Run it from document/testdata/forms with `mise exec -- go run
// generate.go`; the check never runs it, and reads the committed file.
//
// The fields, in the AcroForm's order:
//
//   - applicant.name, a text field under a non-terminal parent, "Ada Lovelace";
//   - applicant.city, a text field whose value is UTF-16BE, "Zürich";
//   - subscribe, a checkbox, checked, its on state /Yes;
//   - newsletter, a checkbox, unchecked, /Off;
//   - contact, a radio group of two buttons, Email and Phone, Phone selected;
//   - colour, a combo box of Red, Green, and Blue, Green selected;
//   - languages, a multi-select list box of English, French, and German, export values en, fr,
//     and de, English and German selected.
package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

func main() {
	if err := os.WriteFile("acroform.pdf", build(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(1)
	}
}

// on and off are a button's appearances: a filled square, and nothing.
const (
	on  = "0 g 3 3 10 10 re f\n"
	off = ""
)

// page is the page's content: a heading and a label beside each field.
const page = `BT /Helv 16 Tf 72 720 Td (Application form) Tj ET
BT /Helv 10 Tf 72 680 Td (Name) Tj ET
BT /Helv 10 Tf 72 650 Td (City) Tj ET
BT /Helv 10 Tf 72 620 Td (Subscribe) Tj ET
BT /Helv 10 Tf 72 590 Td (Newsletter) Tj ET
BT /Helv 10 Tf 72 560 Td (Contact by email or phone) Tj ET
BT /Helv 10 Tf 72 530 Td (Colour) Tj ET
BT /Helv 10 Tf 72 480 Td (Languages) Tj ET
`

// utf16 returns s as a PDF hex string of UTF-16BE with its byte order mark.
func utf16(s string) string {
	var b strings.Builder
	b.WriteString("<FEFF")
	for _, r := range s {
		fmt.Fprintf(&b, "%04X", r)
	}
	b.WriteString(">")
	return b.String()
}

// build returns the PDF: its objects, numbered from 1 in order, then its cross-reference table
// and trailer.
func build() []byte {
	widget := "/Type /Annot /Subtype /Widget /F 4 /P 4 0 R"
	objects := []string{
		// 1: the catalog.
		"<< /Type /Catalog /Pages 2 0 R /AcroForm 3 0 R >>",
		// 2: the page tree.
		"<< /Type /Pages /Kids [4 0 R] /Count 1 >>",
		// 3: the AcroForm.
		"<< /Fields [5 0 R 8 0 R 9 0 R 10 0 R 13 0 R 14 0 R] /DA (/Helv 10 Tf 0 g) " +
			"/DR << /Font << /Helv 15 0 R >> >> /NeedAppearances true >>",
		// 4: the page.
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 16 0 R " +
			"/Resources << /Font << /Helv 15 0 R >> >> " +
			"/Annots [6 0 R 7 0 R 8 0 R 9 0 R 11 0 R 12 0 R 13 0 R 14 0 R] >>",
		// 5: applicant, a non-terminal field.
		"<< /T (applicant) /Kids [6 0 R 7 0 R] >>",
		// 6: applicant.name.
		"<< " + widget + " /Parent 5 0 R /FT /Tx /T (name) /V (Ada Lovelace) " +
			"/Rect [160 675 400 691] /DA (/Helv 10 Tf 0 g) >>",
		// 7: applicant.city.
		"<< " + widget + " /Parent 5 0 R /FT /Tx /T (city) /V " + utf16("Zürich") + " " +
			"/Rect [160 645 400 661] /DA (/Helv 10 Tf 0 g) >>",
		// 8: subscribe, checked.
		"<< " + widget + " /FT /Btn /T (subscribe) /V /Yes /AS /Yes " +
			"/Rect [160 615 176 631] /AP << /N << /Yes 17 0 R /Off 18 0 R >> >> >>",
		// 9: newsletter, unchecked.
		"<< " + widget + " /FT /Btn /T (newsletter) /V /Off /AS /Off " +
			"/Rect [160 585 176 601] /AP << /N << /Yes 17 0 R /Off 18 0 R >> >> >>",
		// 10: contact, a radio group (Radio and NoToggleToOff), Phone selected.
		"<< /FT /Btn /Ff 49152 /T (contact) /V /Phone /Kids [11 0 R 12 0 R] >>",
		// 11: contact's Email button.
		"<< " + widget + " /Parent 10 0 R /AS /Off " +
			"/Rect [160 555 176 571] /AP << /N << /Email 17 0 R /Off 18 0 R >> >> >>",
		// 12: contact's Phone button.
		"<< " + widget + " /Parent 10 0 R /AS /Phone " +
			"/Rect [200 555 216 571] /AP << /N << /Phone 17 0 R /Off 18 0 R >> >> >>",
		// 13: colour, a combo box, Green selected.
		"<< " + widget + " /FT /Ch /Ff 131072 /T (colour) /Opt [(Red) (Green) (Blue)] /V (Green) " +
			"/Rect [160 525 300 541] /DA (/Helv 10 Tf 0 g) >>",
		// 14: languages, a multi-select list box, English and German selected.
		"<< " + widget + " /FT /Ch /Ff 2097152 /T (languages) " +
			"/Opt [[(en) (English)] [(fr) (French)] [(de) (German)]] /V [(en) (de)] /I [0 2] " +
			"/Rect [160 450 300 496] /DA (/Helv 10 Tf 0 g) >>",
		// 15: the font.
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		// 16: the page's content.
		stream("", page),
		// 17 and 18: a button's on and off appearances.
		stream("/Type /XObject /Subtype /Form /BBox [0 0 16 16] ", on),
		stream("/Type /XObject /Subtype /Form /BBox [0 0 16 16] ", off),
	}

	var b bytes.Buffer
	// The header, and a comment of bytes past 127 that marks the file binary.
	b.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objects))
	for i, o := range objects {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}

// stream returns a stream object of content, uncompressed, its dictionary dict and its length.
func stream(dict, content string) string {
	return fmt.Sprintf("<< %s/Length %d >>\nstream\n%sendstream", dict, len(content), content)
}
