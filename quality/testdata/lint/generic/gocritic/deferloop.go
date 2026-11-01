// lint:expect gocritic deferInLoop

package gocritic

import "os"

func DeferLoop(names []string) {
	for _, n := range names {
		f, err := os.Open(n)
		if err != nil {
			continue
		}
		defer f.Close()
	}
}
