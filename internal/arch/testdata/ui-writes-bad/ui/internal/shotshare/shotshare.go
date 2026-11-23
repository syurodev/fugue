package shotshare

import "os"

// A package whose name only starts like the test support package gets no
// exemption.
func Read(path string) ([]byte, error) {
	return os.ReadFile(path)
}
