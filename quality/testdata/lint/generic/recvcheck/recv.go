// lint:expect recvcheck

package recvcheck

type Mixed struct{ n int }

func (m Mixed) Get() int { return m.n }

func (m *Mixed) Set(n int) { m.n = n }
