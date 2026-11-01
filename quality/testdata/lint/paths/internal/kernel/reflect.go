// lint:expect depguard reflect

package kernel

import "reflect"

func Kind(v any) reflect.Kind { return reflect.TypeOf(v).Kind() }
