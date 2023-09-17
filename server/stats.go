package server

import (
	"github.com/sirupsen/logrus"
)

type TrafficEvent struct {
	Key, ClientIP, RemoteAddr, Network string
	In, Out                            int64
}

type TrafficEventConsumer func(e *TrafficEvent)

func (s *TohServer) runTrafficEventConsumeLoop() {
	for e := range s.trafficEventChan {
		if e.In == 0 && e.Out == 0 {
			continue
		}
		logrus.
			WithField("stats_in_bytes", e.In).
			WithField("stats_out_bytes", e.Out).
			WithField("stats_key", e.Key).
			WithField("stats_net", e.Network).
			WithField("stats_in", e.ClientIP).
			WithField("stats_out", e.RemoteAddr).
			Info()
		s.acl.UpdateBytesUsage(e.Key, uint64(e.In), uint64(e.Out))
	}
}
