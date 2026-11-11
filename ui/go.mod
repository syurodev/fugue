module github.com/syurodev/fugue/ui

go 1.27.4

require (
	connectrpc.com/connect v1.21.1
	gioui.org v0.10.3
	github.com/syurodev/fugue v0.0.0
	go.uber.org/goleak v1.3.0
	golang.org/x/image v0.47.0
)

replace github.com/syurodev/fugue => ../

require (
	gioui.org/shader v1.0.9 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	golang.org/x/exp/shiny v0.0.0-20250408133849-7e4ce0ab07d0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
