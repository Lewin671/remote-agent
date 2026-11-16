module github.com/Lewin671/remote-agent/clientcore

go 1.26.11

require (
	github.com/Lewin671/remote-agent v0.0.0
	google.golang.org/protobuf v1.36.10
)

require (
	golang.org/x/mobile v0.0.0-20260821190718-4776eadac327 // indirect
	golang.org/x/mod v0.39.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
)

replace github.com/Lewin671/remote-agent => ../

tool (
	golang.org/x/mobile/cmd/gobind
	golang.org/x/mobile/cmd/gomobile
)
