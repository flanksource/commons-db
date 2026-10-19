package terminal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"iter"
	"os"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

func rowsOf(events ...Event) iter.Seq[Event] { return slices.Values(events) }

func intPtr(v int) *int { return &v }

// sampleRows are the events behind testdata/sample.cast, which gavel's
// record.WriteCast wrote from the same capture.
func sampleRows() iter.Seq[Event] {
	at := func(offset float64, kind, data string) Event {
		return Event{Offset: offset, Kind: kind, Data: data, Width: 80, Height: 24}
	}
	exit := at(1.5, KindExit, "")
	exit.ExitCode, exit.Truncated, exit.Command = intPtr(3), true, "/bin/sh -c echo hi"
	return rowsOf(
		at(0.01, KindOutput, "hi\r\n"),
		at(0.5, KindSnapshot, "hi"),
		at(0.75, KindOutput, "\x1b[32mhi\x1b[0m \"quoted\" <é>\r\n"),
		at(1.2, KindOutput, "bye\r\n"),
		at(1.5, KindSnapshot, "hi\nhi\nbye"),
		exit,
	)
}

var _ = Describe("cast export", func() {
	It("writes the bytes gavel's WriteCast wrote for the same capture", func() {
		golden, err := os.ReadFile("testdata/sample.cast")
		Expect(err).ToNot(HaveOccurred())
		var out bytes.Buffer
		Expect(terminal{}.Export(Cast.Name, sampleRows(), &out)).To(Succeed())
		Expect(out.String()).To(Equal(string(golden)))
	})

	// asciinema v2 is line-delimited: a header object, then one array per event.
	// Anything else stops `asciinema play` from opening the cast at all.
	It("is line-delimited asciinema v2 with the exit code, final screen and duplicates in the header", func() {
		var out bytes.Buffer
		Expect(terminal{}.Export(Cast.Name, sampleRows(), &out)).To(Succeed())
		scanner := bufio.NewScanner(&out)
		Expect(scanner.Scan()).To(BeTrue(), "a header line")
		var header struct {
			Version, Width, Height int
			Duration               float64
			Command                string
			Gavel                  struct {
				ExitCode   int `json:"exit_code"`
				Final      string
				Duplicates []struct {
					Text  string
					Count int
				}
				Snapshots [][]json.RawMessage
			} `json:"_gavel"`
		}
		Expect(json.Unmarshal(scanner.Bytes(), &header)).To(Succeed())
		Expect(header.Version).To(Equal(2))
		Expect(header.Width).To(Equal(80))
		Expect(header.Height).To(Equal(24))
		Expect(header.Duration).To(BeNumerically("~", 1.5, 0.001))
		Expect(header.Command).To(Equal("/bin/sh -c echo hi"))
		Expect(header.Gavel.ExitCode).To(Equal(3))
		Expect(header.Gavel.Final).To(Equal("hi\nhi\nbye"))
		Expect(header.Gavel.Duplicates).To(HaveLen(1))
		Expect(header.Gavel.Duplicates[0].Count).To(Equal(2))
		Expect(header.Gavel.Snapshots).To(HaveLen(2))
		var events [][]json.RawMessage
		for scanner.Scan() {
			var event []json.RawMessage
			Expect(json.Unmarshal(scanner.Bytes(), &event)).To(Succeed(), "each line after the header is an event array")
			events = append(events, event)
		}
		Expect(events).To(HaveLen(3))
		Expect(string(events[0][0])).To(Equal("0.01"), "offsets are seconds, as the format requires")
		Expect(string(events[0][1])).To(Equal(`"o"`))
	})

	It("writes a resize as asciinema's r event, and a window with no exit row as exit code -1", func() {
		rows := rowsOf(
			Event{Offset: 0.1, Kind: KindOutput, Data: "x", Width: 80, Height: 24},
			Event{Offset: 0.2, Kind: KindResize, Width: 100, Height: 30},
		)
		var out bytes.Buffer
		Expect(terminal{}.Export(Cast.Name, rows, &out)).To(Succeed())
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		Expect(lines[0]).To(ContainSubstring(`"width":80,"height":24`))
		Expect(lines[0]).To(ContainSubstring(`"exit_code":-1`))
		Expect(lines[2]).To(Equal(`[0.2,"r","100x30"]`))
	})

	It("refuses an empty window and a row of unknown kind", func() {
		Expect(terminal{}.Export(Cast.Name, rowsOf(), &bytes.Buffer{})).To(MatchError(ContainSubstring("no terminal rows")))
		Expect(terminal{}.Export(Cast.Name, rowsOf(Event{Kind: "bogus", Width: 1, Height: 1}), &bytes.Buffer{})).
			To(MatchError(ContainSubstring(`unknown kind "bogus"`)))
	})
})

var _ = Describe("cast summary", func() {
	It("summarizes the capture as gavel's cast CEL root does, without a path", func() {
		summary, err := terminal{}.Summarize(sampleRows())
		Expect(err).ToNot(HaveOccurred())
		Expect(summary).To(MatchAllKeys(Keys{
			"events": Equal(3), "duration_ms": Equal(int64(1500)), "exit_code": Equal(3),
			"width": Equal(80), "height": Equal(24), "final": Equal("hi\nhi\nbye"),
			// A count rather than the timeline: a screen is kilobytes and a run
			// holds hundreds, so the scrubbable version stays in the cast.
			"snapshots":      Equal(2),
			"duplicates":     Equal([]map[string]any{{"text": "hi", "count": 2}}),
			"has_duplicates": BeTrue(), "truncated": BeTrue(),
		}))
	})

	// Every key is present on an empty window too, so `cast.has_duplicates == false`
	// is an assertion rather than an evaluation error.
	It("holds every key on an empty window", func() {
		summary, err := terminal{}.Summarize(rowsOf())
		Expect(err).ToNot(HaveOccurred())
		Expect(summary).To(MatchAllKeys(Keys{
			"events": Equal(0), "duration_ms": Equal(int64(0)), "exit_code": Equal(NoExitCode),
			"width": Equal(0), "height": Equal(0), "final": Equal(""), "snapshots": Equal(0),
			"duplicates": BeEmpty(), "has_duplicates": BeFalse(), "truncated": BeFalse(),
		}))
	})
})
