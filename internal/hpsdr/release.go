package hpsdr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// RadioState is what a radio at a given address reports, for the "release radio" function.
type RadioState struct {
	Found bool   `json:"found"`
	Busy  bool   `json:"busy"`  // the radio says it is streaming to some host
	Ours  bool   `json:"ours"`  // this program is receiving from it (so it is not stuck)
	Radio string `json:"radio"` // description of the radio
}

// Probe asks the radio at addr (an IP address) whether it is streaming.
func Probe(ctx context.Context, addr string, port int) (RadioState, error) {
	if addr == "" {
		return RadioState{}, errors.New("no radio address given")
	}
	if port == 0 {
		port = DefaultPort
	}
	found, _, err := discover(ctx, addr, port, 1500*time.Millisecond)
	if err != nil {
		return RadioState{}, err
	}
	ra, err := resolve(addr, port)
	if err != nil {
		return RadioState{}, err
	}
	st := RadioState{Ours: lookup(ra) != nil}
	if len(found) > 0 {
		st.Found, st.Busy, st.Radio = true, found[0].Busy, found[0].String()
	}
	return st, nil
}

// Release tells the radio at addr to stop streaming (the OpenHPSDR stop command). It refuses when this program is
// itself receiving from that radio. It returns the radio's state afterwards.
func Release(ctx context.Context, addr string, port int) (RadioState, error) {
	if port == 0 {
		port = DefaultPort
	}
	st, err := Probe(ctx, addr, port)
	if err != nil {
		return st, err
	}
	switch {
	case !st.Found:
		return st, errors.New("the radio did not answer")
	case st.Ours:
		return st, errors.New("this program is receiving from that radio itself; stop the channel that uses it instead")
	case !st.Busy:
		return st, errors.New("the radio does not report being in use")
	}
	ra, err := resolve(addr, port)
	if err != nil {
		return st, err
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return st, err
	}
	defer conn.Close()
	for i := 0; i < 3; i++ { // (a few times, in case of a lost packet)
		if _, err := conn.WriteToUDP(startPacket(false), ra); err != nil {
			return st, fmt.Errorf("sending the stop command: %w", err)
		}
		time.Sleep(150 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	return Probe(ctx, addr, port)
}
