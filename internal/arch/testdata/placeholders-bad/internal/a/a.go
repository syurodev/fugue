package a

import (
	"errors"
	"fmt"
)

type T struct{}

func Nothing() (*int, error) { return nil, nil }

func Empty() {}

func Todo() error { return errors.New("not implemented") }

func Wrapped() error { return fmt.Errorf("feature: Not Implemented yet") }

func (T) Method() {}

func Panics() { panic("not implemented") }
