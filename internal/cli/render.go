package cli

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// Table renders aligned columns. It measures width in runes rather than bytes
// so the CJK repo and branch names in a real inventory line up.
type Table struct {
	head  []string
	rows  [][]string
	style cliStyle
}

// NewTable starts a table with the given header.
func NewTable(header ...string) *Table { return &Table{head: header} }

// Add appends a row. Short rows are padded, long ones are truncated to the
// header width so a stray field can never shift the whole table.
func (t *Table) Add(cells ...string) {
	row := make([]string, len(t.head))
	copy(row, cells)
	t.rows = append(t.rows, row)
}

// Len reports the number of data rows.
func (t *Table) Len() int { return len(t.rows) }

// Render writes the table, sizing every column to its widest cell.
func (t *Table) Render(w io.Writer) {
	if len(t.rows) == 0 {
		return
	}
	widths := make([]int, len(t.head))
	for i, h := range t.head {
		widths[i] = width(h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			if n := width(c); n > widths[i] {
				widths[i] = n
			}
		}
	}
	head := make([]string, len(t.head))
	for i, cell := range t.head {
		head[i] = t.style.header(cell)
	}
	writeRow(w, head, widths)
	for _, r := range t.rows {
		writeRow(w, r, widths)
	}
}

func writeRow(w io.Writer, cells []string, widths []int) {
	var b strings.Builder
	for i, c := range cells {
		if i == len(cells)-1 {
			b.WriteString(c) // never pad the last column
			break
		}
		b.WriteString(c)
		b.WriteString(strings.Repeat(" ", widths[i]-width(c)+2))
	}
	fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
}

// width counts display columns, treating wide CJK runes as two.
func width(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if end := ansiEnd(s, i); end > i {
			i = end
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if isWide(r) {
			n += 2
		} else {
			n++
		}
		i += size
	}
	return n
}

func ansiEnd(s string, start int) int {
	if start+2 > len(s) || s[start] != '\x1b' || s[start+1] != '[' {
		return start
	}
	for i := start + 2; i < len(s); i++ {
		if s[i] >= 0x40 && s[i] <= 0x7e {
			return i + 1
		}
	}
	return start
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if end := ansiEnd(s, i); end > i {
			i = end
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0xA4CF, // CJK radicals through Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE6F, // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x20000 && r <= 0x3FFFD: // CJK extension planes
		return true
	}
	return false
}

// truncate shortens s to at most n display columns, marking the cut with "…".
func truncate(s string, n int) string {
	if n <= 0 || width(s) <= n {
		return s
	}
	var b strings.Builder
	used := 0
	hasANSI := false
	for i := 0; i < len(s); {
		if end := ansiEnd(s, i); end > i {
			hasANSI = true
			b.WriteString(s[i:end])
			i = end
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := 1
		if isWide(r) {
			rw = 2
		}
		if used+rw > n-1 {
			break
		}
		b.WriteRune(r)
		used += rw
		i += size
	}
	b.WriteString("…")
	if hasANSI {
		b.WriteString(ansiReset)
	}
	return b.String()
}

// dash renders an empty value as a visible placeholder, so a blank column is
// never mistaken for a rendering bug.
func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// humanAge renders a duration the way a changelog would: "3d", "2w", "5mo".
func humanAge(d time.Duration) string {
	switch {
	case d <= 0:
		return "—"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dw", int(d.Hours()/24/7))
	default:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	}
}

// shellQuote makes a path safe to paste into a POSIX shell command.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r == '/' || r == '.' || r == '-' || r == '_' || r == '~' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			safe = false
			break
		}
	}
	if safe && utf8.ValidString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
