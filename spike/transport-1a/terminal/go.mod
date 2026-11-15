module github.com/Lewin671/remote-agent/spike/transport1aterminal

go 1.26.11

require (
	example.com/transport-sdk-lab v0.0.0
	github.com/Lewin671/remote-agent v0.0.0
	google.golang.org/protobuf v1.36.10
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.48.0 // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.60.1 // indirect
)

replace github.com/Lewin671/remote-agent => ../../..

replace example.com/transport-sdk-lab => ../go
