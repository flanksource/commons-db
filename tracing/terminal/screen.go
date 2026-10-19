package terminal

// The screen emulation is ported from gavel's fixtures/ansi_screen.go: a
// deliberately minimal settle of the sequences clicky's renderer uses.

import "strings"

type parserState uint8

const (
	ansiText parserState = iota
	ansiEscape
	ansiCSI
	ansiOSC
	ansiOSCEscape
)

type screen struct {
	width  int
	height int
	rows   [][]byte
	row    int
	col    int

	state        parserState
	params       [2]int
	paramPresent [2]bool
	paramIndex   int
	paramInvalid bool
}

func newScreen(width, height int) *screen {
	return &screen{
		width:  width,
		height: height,
		rows:   [][]byte{nil},
	}
}

func (s *screen) Write(data []byte) {
	for _, b := range data {
		s.writeByte(b)
	}
}

func (s *screen) writeByte(b byte) {
	switch s.state {
	case ansiEscape:
		s.writeEscape(b)
	case ansiCSI:
		s.writeCSI(b)
	case ansiOSC:
		switch b {
		case 0x07:
			s.state = ansiText
		case 0x1b:
			s.state = ansiOSCEscape
		}
	case ansiOSCEscape:
		switch b {
		case '\\':
			s.state = ansiText
		case 0x1b:
			s.state = ansiOSCEscape
		default:
			s.state = ansiOSC
		}
	default:
		s.writeText(b)
	}
}

func (s *screen) writeEscape(b byte) {
	switch b {
	case '[':
		s.resetCSI()
		s.state = ansiCSI
	case ']':
		s.state = ansiOSC
	case 0x1b:
		s.state = ansiEscape
	default:
		s.state = ansiText
	}
}

func (s *screen) writeCSI(b byte) {
	switch {
	case b >= '0' && b <= '9':
		if s.paramIndex < len(s.params) {
			s.paramPresent[s.paramIndex] = true
			s.params[s.paramIndex] = s.params[s.paramIndex]*10 + int(b-'0')
		}
	case b == ';':
		s.paramIndex++
	case b == '?':
	case b == ' ':
		s.paramInvalid = true
	default:
		s.executeCSI(b)
		s.state = ansiText
	}
}

func (s *screen) resetCSI() {
	s.params = [2]int{}
	s.paramPresent = [2]bool{}
	s.paramIndex = 0
	s.paramInvalid = false
}

func (s *screen) param(index, defaultValue int) int {
	if s.paramInvalid || index >= len(s.params) || !s.paramPresent[index] {
		return defaultValue
	}
	return s.params[index]
}

func (s *screen) executeCSI(final byte) {
	n := s.param(0, 1)
	switch final {
	case 'A':
		s.row -= n
		if s.row < 0 {
			s.row = 0
		}
	case 'B':
		s.moveCursorDown(n)
	case 'E':
		s.moveCursorDown(n)
		s.col = 0
	case 'F':
		s.row -= n
		if s.row < 0 {
			s.row = 0
		}
		s.col = 0
	case 'G':
		s.col = n - 1
		s.clampCol()
	case 'H', 'f':
		s.row = n - 1
		s.col = s.param(1, 1) - 1
		if s.row < 0 {
			s.row = 0
		}
		if s.height > 0 && s.row >= s.height {
			s.row = s.height - 1
		}
		s.clampCol()
		s.ensureRow(s.row)
	case 'K':
		s.eraseLine(s.param(0, 0))
	case 'J':
		if s.param(0, 0) == 2 {
			s.rows = [][]byte{nil}
			s.row = 0
			s.col = 0
		}
	}
}

func (s *screen) writeText(b byte) {
	switch b {
	case 0x1b:
		s.state = ansiEscape
	case '\r':
		s.col = 0
	case '\n':
		s.lineFeed()
		s.col = 0
	case '\b':
		if s.col > 0 {
			s.col--
		}
	case '\t':
		next := (s.col/8 + 1) * 8
		for s.col < next {
			s.writePrintable(' ')
		}
	default:
		if b >= 0x20 {
			s.writePrintable(b)
		}
	}
}

func (s *screen) writePrintable(b byte) {
	if s.width > 0 && s.col >= s.width {
		s.lineFeed()
		s.col = 0
	}
	s.ensureCol(s.row, s.col)
	if s.col < len(s.rows[s.row]) {
		s.rows[s.row][s.col] = b
	} else {
		s.rows[s.row] = append(s.rows[s.row], b)
	}
	s.col++
}

func (s *screen) lineFeed() {
	if s.height > 0 && s.row >= s.height-1 {
		if len(s.rows) < s.height {
			s.rows = append(s.rows, nil)
			s.row++
			return
		}
		copy(s.rows, s.rows[1:])
		s.rows[len(s.rows)-1] = nil
		s.row = s.height - 1
		return
	}
	s.row++
	s.ensureRow(s.row)
}

func (s *screen) moveCursorDown(n int) {
	s.row += n
	if s.height > 0 && s.row >= s.height {
		s.row = s.height - 1
	}
	s.ensureRow(s.row)
}

func (s *screen) ensureRow(row int) {
	for row >= len(s.rows) {
		s.rows = append(s.rows, nil)
	}
}

func (s *screen) ensureCol(row, col int) {
	s.ensureRow(row)
	for len(s.rows[row]) < col {
		s.rows[row] = append(s.rows[row], ' ')
	}
}

func (s *screen) clampCol() {
	if s.col < 0 {
		s.col = 0
	}
	if s.width > 0 && s.col >= s.width {
		s.col = s.width - 1
	}
}

func (s *screen) eraseLine(mode int) {
	s.ensureRow(s.row)
	switch mode {
	case 0:
		if s.col < len(s.rows[s.row]) {
			s.rows[s.row] = s.rows[s.row][:s.col]
		}
	case 1:
		s.ensureCol(s.row, s.col)
		for i := 0; i < s.col && i < len(s.rows[s.row]); i++ {
			s.rows[s.row][i] = ' '
		}
	case 2:
		s.rows[s.row] = nil
	}
}

func (s *screen) String() string {
	var output strings.Builder
	for i, row := range s.rows {
		output.Write(row)
		if i < len(s.rows)-1 {
			output.WriteByte('\n')
		}
	}
	return output.String()
}

// resize changes the viewport the cursor is bounded by; rows already drawn stay.
func (s *screen) resize(width, height int) {
	s.width, s.height = width, height
	s.clampCol()
	if s.height > 0 && s.row >= s.height {
		s.row = s.height - 1
	}
}

// DupLine is a line a settled screen shows more than once.
type DupLine struct {
	Text  string
	Count int
}

// duplicateSettledLines returns every non-empty line that appears more than
// once in a settled screen, trimmed so spinner frames that differ only in
// padding count as one, in order of first appearance.
func duplicateSettledLines(settled string) []DupLine {
	counts := make(map[string]int)
	order := []string{}
	for _, line := range strings.Split(settled, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if _, seen := counts[trimmed]; !seen {
			order = append(order, trimmed)
		}
		counts[trimmed]++
	}
	var dups []DupLine
	for _, line := range order {
		if counts[line] > 1 {
			dups = append(dups, DupLine{Text: line, Count: counts[line]})
		}
	}
	return dups
}
