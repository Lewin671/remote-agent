package main

import (
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/antigravity"
)

func analyzeNative(s string) agent.Screen { return antigravity.New("", "").Analyze(s) }
