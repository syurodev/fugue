// lint:expect revive receiver name

package revive

type Thing struct{}

func (this Thing) Name() string { return "" }
