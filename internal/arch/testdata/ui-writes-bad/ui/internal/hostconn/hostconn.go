package hostconn

import (
	"net"
	"os"

	_ "github.com/syurodev/fugue/internal/platform/clock"
)

func Write(dataDir string) error {
	return os.WriteFile(dataDir+"/host.json", nil, 0o600)
}

func Dial(addr string) (net.Conn, error) {
	return net.Dial("unix", addr)
}
