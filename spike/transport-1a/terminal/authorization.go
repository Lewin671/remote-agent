package main

import "github.com/Lewin671/remote-agent/shared/devicesec"

// A retained response is scoped to the authority and logical owner that
// admitted it. Physical relay/direct legs do not change that owner.
type authorization struct {
	client, machine      devicesec.Member
	head                 devicesec.Hash
	instance             string
	generation, deadline uint64
}

func (s *Stream) authorization() authorization {
	head, _ := s.sec.state.Hash()
	return authorization{s.sec.client, s.sec.machine, head, s.instance, s.controlGeneration, s.now + 10000}
}
func (s *Stream) authorized(a authorization) bool {
	if s.now >= a.deadline || s.instance != a.instance || s.controlGeneration != a.generation || s.sec.lease.Valid(s.sec.state, s.now) != nil {
		return false
	}
	head, e := s.sec.state.Hash()
	if e != nil || head != a.head {
		return false
	}
	c, cok := s.sec.state.Core.Member(a.client.ID)
	m, mok := s.sec.state.Core.Member(a.machine.ID)
	return cok && mok && c == a.client && m == a.machine && s.sec.client == c && s.sec.machine == m
}
func (s *Stream) markACK(a authorization) {
	if s.dirtyACK == 0 {
		s.ackAuthorization = a
	}
	s.dirtyACK++
}
func (s *Stream) immediateACK(a authorization) {
	if s.authorized(a) {
		s.acks++
	}
}
