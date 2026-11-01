// lint:expect nilerr

package nilerr

func Swallow(err error) error {
	if err != nil {
		return nil
	}
	return nil
}
