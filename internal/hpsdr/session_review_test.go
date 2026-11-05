package hpsdr

// Review tests (not part of the project). See the review report.

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// Finding H1: when the radio's sequence numbers start again from 0 while navtexd keeps its session (the radio's server was
// restarted by another start packet, the FPGA reloaded, or a long run wrapped), every packet has Seq < next and is dropped as
// "late or duplicate". lastData is refreshed before that test, so the 10 s silence check never fires either: the channel
// shows as running and gets no samples until the radio's counter catches up with the old one (possibly days).
func TestReview_SequenceRestartStallsForever(t *testing.T) {
	f := newFake(t)
	s, err := Open(context.Background(), Options{Addr: "127.0.0.1", Port: f.port(), RateHz: 48000, FreqHz: 515000})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	readN(t, s, 126*2000) // about 5 s: the radio's counter is near 2000
	f.mu.Lock()
	f.stopped = true // the fake's own stream ends
	f.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	to := s.s.conn.LocalAddr().(*net.UDPAddr)
	to.IP = net.IPv4(127, 0, 0, 1)
	go func() { // the radio streams again, counting from 0
		for seq := uint32(0); seq < 1000; seq++ {
			p := make([]byte, packetLen)
			p[0], p[1], p[2], p[3] = 0xef, 0xfe, 1, 6
			binary.BigEndian.PutUint32(p[4:], seq)
			for fr := 0; fr < 2; fr++ {
				b := p[8+fr*frameLen:]
				b[0], b[1], b[2] = 0x7f, 0x7f, 0x7f
				b[8] = 0x10
			}
			f.conn.WriteToUDP(p, to)
			time.Sleep(2600 * time.Microsecond)
		}
	}()
	go func() { // keep the source drained
		buf := make([]complex64, 4096)
		for {
			if _, err := s.Read(buf); err != nil {
				return
			}
		}
	}()
	time.Sleep(3500 * time.Millisecond)
	st := s.Stats()
	t.Logf("stats: %+v", st)
	if st.Reordered > 900 {
		t.Fatalf("after the radio's sequence restarted at 0, %d of 1000 packets were discarded as late/duplicate: no samples, no error", st.Reordered)
	}
}
