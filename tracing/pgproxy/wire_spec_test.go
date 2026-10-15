// Specs for the wire decoding the proxy records from: framing across chunk
// boundaries, oversized messages, and pairing statements with their answers.

package pgproxy

import (
	"encoding/binary"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/flanksource/commons-db/connection"
)

type sniffed struct {
	typ  byte
	body string
}

// wireMessage builds one framed protocol message: type byte, big-endian length
// covering the length field itself, then the body.
func wireMessage(typ byte, body string) []byte {
	msg := make([]byte, 5+len(body))
	msg[0] = typ
	binary.BigEndian.PutUint32(msg[1:5], uint32(4+len(body)))
	copy(msg[5:], body)
	return msg
}

var _ = Describe("pg wire sniffer", func() {
	var seen []sniffed
	var sniffer *pgSniffer

	BeforeEach(func() {
		seen = nil
		sniffer = &pgSniffer{onMessage: func(typ byte, body []byte) { seen = append(seen, sniffed{typ, string(body)}) }}
	})

	for _, chunk := range []int{1, 3, 7, 1000} {
		It(fmt.Sprintf("frames messages across %d-byte chunk boundaries", chunk), func() {
			stream := append(wireMessage('Q', "SELECT 1"), wireMessage('Q', "SELECT 2")...)
			for offset := 0; offset < len(stream); offset += chunk {
				end := min(offset+chunk, len(stream))
				n, err := sniffer.Write(stream[offset:end])
				Expect(err).ToNot(HaveOccurred())
				Expect(n).To(Equal(end-offset), "a sniffer must report every byte written or io.Copy stops")
			}
			Expect(seen).To(Equal([]sniffed{{'Q', "SELECT 1"}, {'Q', "SELECT 2"}}))
		})
	}

	It("drops an oversized message spanning several writes and stays framed", func() {
		oversized := make([]byte, 5+maxWireMessage)
		oversized[0] = 'd'
		binary.BigEndian.PutUint32(oversized[1:5], uint32(4+maxWireMessage))
		half := len(oversized) / 2
		for _, part := range [][]byte{oversized[:half], oversized[half:], wireMessage('Q', "SELECT 1")} {
			_, err := sniffer.Write(part)
			Expect(err).ToNot(HaveOccurred())
		}
		Expect(seen).To(Equal([]sniffed{{'Q', "SELECT 1"}}))
	})

	It("stops sniffing once a length says the stream is no longer framed", func() {
		_, err := sniffer.Write([]byte{'Q', 0, 0, 0, 1, 'x', 'y'})
		Expect(err).ToNot(HaveOccurred())
		Expect(seen).To(BeEmpty())
	})
})

var _ = Describe("pg session", func() {
	var emitted []connection.Statement
	newSession := func(params bool) *pgSession {
		emitted = nil
		return newPGSession("db", params, func(s connection.Statement) { emitted = append(emitted, s) })
	}

	It("records work still pending at ReadyForQuery as issued with no row count", func() {
		session := newSession(false)
		session.frontend('Q', []byte("SELECT 1\x00"))
		session.backend('Z', []byte{'I'})
		Expect(emitted).To(HaveLen(1))
		Expect(emitted[0]).To(MatchFields(IgnoreExtras, Fields{
			"Connection": Equal("db"), "Driver": Equal("postgres"), "SQL": Equal("SELECT 1"), "Rows": BeEquivalentTo(-1),
		}))
	})

	It("ignores a completion nothing was waiting for", func() {
		session := newSession(false)
		session.backend('C', []byte("SELECT 1\x00"))
		Expect(emitted).To(BeEmpty())
	})

	DescribeTable("reads the row count off a command tag",
		func(tag string, want int64) { Expect(rowsFromTag(tag)).To(Equal(want)) },
		Entry("select", "SELECT 3", int64(3)),
		Entry("insert", "INSERT 0 7", int64(7)),
		Entry("no count", "BEGIN", int64(-1)),
		Entry("non-numeric", "SET x", int64(-1)),
	)
})
